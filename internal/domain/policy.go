package domain

import (
	"slices"
	"strings"
	"time"
)

// ResolvedPolicy is the effective configuration for one hook invocation, after
// all matching rules have been stacked (§5.2). It is what the kernel layer
// translates into axi-go Action registrations and budgets (§5.4), and what
// `warden policy explain` renders.
type ResolvedPolicy struct {
	Hook                Hook
	Execution           ExecutionConfig
	ConfigurationDigest string
	// Steps is the final ordered step list for this run.
	Steps []StepName
	// Agents maps a step to its resolved coding-agent binary. Absent = default.
	Agents map[StepName]string
	// AutoFix maps a step to its retry budget (MaxCapabilityInvocations).
	AutoFix map[StepName]int
	// RequireApproval forces an approval pause for the run.
	RequireApproval bool
	// Commands maps a shell-backed step to its command line.
	Commands map[string]string
	// AgentCommands maps an agent name to its invocation command template.
	AgentCommands map[string]string
	// Risk is the classification that drove rule matching.
	Risk Risk
	// MatchedRules names the rules that contributed, in declaration order,
	// for provenance in run notes (§9) and `policy explain`.
	MatchedRules []string
	// Parallel enables concurrent execution of independent (read-only) steps.
	// When true, consecutive steps that don't write to the worktree run at once,
	// so the gate is as slow as the slowest check, not their sum (§4.4).
	Parallel bool
	// Timeouts maps a step to its max run duration; zero means no limit.
	Timeouts map[StepName]time.Duration
	// Cache maps a step to its declared input path globs (see Config.Cache).
	PredictiveCache []string
	Cache           map[StepName][]string
	// MaterializeDeps is true when this run includes a step that needs gitignored
	// dependency dirs materialized as real files in the worktree rather than
	// symlinked (see Config.MaterializeDeps). Resolved from the run's step set.
	MaterializeDeps bool
	// SecurityScan is the resolved security-scan gate configuration.
	SecurityScan SecurityScanConfig
	// Diagnostics maps a shell check to the repo-relative SARIF file its
	// command writes (Config.Diagnostics). The JSON tag keeps policy_explain
	// output unchanged for a repo that declares none.
	Diagnostics map[StepName]string `json:"Diagnostics,omitempty"`
	// WriteSteps are steps the config declared as tree-mutating (Config.Writes).
	// They run as sequential barriers, never in a parallel batch — the escape
	// hatch for a custom step (codegen, formatter) that writes the worktree.
	WriteSteps map[StepName]bool
	// Profile is the compliance profile in force, or nil when the config names
	// none. The JSON tag keeps the MCP policy_explain output of an unprofiled
	// repo exactly as it was.
	Profile *ResolvedProfile `json:"Profile,omitempty"`
}

// ResolvedProfile is the compliance profile a policy was resolved under, with
// each floor's status against the effective config, for `policy explain`.
type ResolvedProfile struct {
	// Name is the canonical "name@version", e.g. "strict@1".
	Name   string
	Level  string
	Digest string
	Floors []ProfileFloorStatus
}

// CachePaths returns the declared input globs for a step, or nil.
func (p ResolvedPolicy) CachePaths(s StepName) []string {
	if p.Cache == nil {
		return nil
	}
	return p.Cache[s]
}

// Cacheable reports whether a step may be cache-skipped: it must declare input
// paths and must not mutate the worktree (caching a mutating step would skip its
// writes), so it reuses the Concurrent predicate.
func (p ResolvedPolicy) Cacheable(s StepName) bool {
	return len(p.CachePaths(s)) > 0 && p.Concurrent(s)
}

// TimeoutFor returns the configured timeout for a step, or 0 (no limit).
func (p ResolvedPolicy) TimeoutFor(s StepName) time.Duration {
	if p.Timeouts == nil {
		return 0
	}
	return p.Timeouts[s]
}

// Batches groups the resolved steps into an ordered execution schedule. A batch
// of one runs on its own; a batch of many runs concurrently. Consecutive
// Concurrent steps collapse into one parallel batch; a step that mutates the
// worktree (rebase, an auto-fix step) is a barrier — it runs alone and flushes
// the batches around it, preserving declared order and every write ordering.
// With Parallel off, every step is its own batch (the classic pipeline).
func (p ResolvedPolicy) Batches() [][]StepName {
	var batches [][]StepName
	var cur []StepName
	flush := func() {
		if len(cur) > 0 {
			batches = append(batches, cur)
			cur = nil
		}
	}
	for _, s := range p.Steps {
		if p.Parallel && p.Concurrent(s) {
			cur = append(cur, s)
			continue
		}
		flush()
		batches = append(batches, []StepName{s})
	}
	flush()
	return batches
}

// Concurrent reports whether step is safe to run alongside other steps in the
// shared worktree. A step qualifies only when Warden can be confident it does
// not mutate the tracked tree: it must not rewrite history (rebase), be the
// terminal push, carry an auto-fix budget (a non-zero budget writes), be a
// coding-agent step (agents routinely edit files — see writesTree), or be
// declared tree-writing in config (Config.Writes). Everything else — shell
// checks like lint/test and custom commands the author owns — runs concurrently.
func (p ResolvedPolicy) Concurrent(s StepName) bool {
	return s != StepPush && !p.KeepsWrites(s)
}

// KeepsWrites reports whether a step's worktree writes must be PRESERVED and
// ordered, so it runs as a sequential barrier in the canonical worktree: a
// history rewrite (rebase), an auto-fix step (its fixes are folded back into the
// tree), or a step declared under `writes:`. Every other step is isolatable — it
// may run in a parallel batch, each step in its own ephemeral worktree whose
// writes are discarded (only findings are kept), so even a tree-touching agent
// (review/document/intent) can run concurrently without racing a sibling.
func (p ResolvedPolicy) KeepsWrites(s StepName) bool {
	return s == StepRebase || p.AutoFixBudget(s) > 0 || p.WriteSteps[s]
}

// WritesTree is the single source of truth for "does this step mutate the
// tracked worktree at all" — kept or discarded — and drives the kernel's axi
// effect level. It is KeepsWrites plus the coding-agent steps (a built-in agent
// or a rule-assigned one), which edit files even though a parallel run discards
// those writes. Keeping this and the scheduler's KeepsWrites derived from the
// same place is what stops the two from drifting (that drift — the scheduler
// treating a tree-writing agent as read-only in a SHARED worktree — was the root
// of the race fixed in v0.10.1; per-step isolation now makes agent concurrency
// safe).
func (p ResolvedPolicy) WritesTree(s StepName) bool {
	return p.KeepsWrites(s) || s.IsAgentStep() || p.AgentFor(s) != ""
}

// AgentFor returns the resolved agent for a step, or "" if the default applies.
func (p ResolvedPolicy) AgentFor(s StepName) string {
	if p.Agents == nil {
		return ""
	}
	return p.Agents[s]
}

// AgentBoundaryFor returns what the compliance profile in force enforces around
// step s at run time: the zero AgentBoundary without a profile, under a
// profile that enforces nothing (permissive), and for a step that is not an
// agent step. An agent step is a built-in one (intent, review, document) or a
// step this run hands to an agent.
//
// A step whose writes the config explicitly keeps (writes:, or an auto_fix
// budget) is not isolated: isolating it would silently discard the writes the
// config authorized. standard@1 allows that and strict@1 refuses it at load
// (the no-agent-writes floor), so under strict every agent step is isolated.
func (p ResolvedPolicy) AgentBoundaryFor(s StepName) AgentBoundary {
	if p.Profile == nil || (!s.IsAgentStep() && p.AgentFor(s) == "") {
		return AgentBoundary{}
	}
	prof, err := ParseComplianceProfile(p.Profile.Name)
	if err != nil {
		return AgentBoundary{}
	}
	b := prof.AgentBoundary()
	if !b.Active() {
		return AgentBoundary{}
	}
	if p.KeepsWrites(s) {
		b.Isolate = false
	}
	return b
}

// AutoFixBudget returns the retry budget for a step (0 = no auto-fix retries).
func (p ResolvedPolicy) AutoFixBudget(s StepName) int {
	if p.AutoFix == nil {
		return 0
	}
	return p.AutoFix[s]
}

// AuthorizesFix reports whether any step in this run was granted an auto-fix
// budget, i.e. was authorized to mutate the worktree and have those edits
// written back to the developer's tree. When false the run is read-only: no
// step may legitimately write back, so a passing pre-commit must capture no fix
// patch at all — this is the enforcement boundary on who may mutate the tree,
// not just a bound on subprocess retry counts.
func (p ResolvedPolicy) AuthorizesFix() bool {
	for _, budget := range p.AutoFix {
		if budget > 0 {
			return true
		}
	}
	return false
}

// Predictable excludes native, scanner, agent and tree-writing semantics.
func (p ResolvedPolicy) Predictable(s StepName) bool {
	return p.Hook == PreCommit && p.Execution.ContainerEnabled() && (p.Execution.Network == "" || p.Execution.Network == "none") && s != StepSecurityScan && (s == StepLint || s == StepTest || !s.IsBuiltin()) && !p.WritesTree(s) && slices.Contains(p.PredictiveCache, string(s)) && strings.TrimSpace(p.Commands[string(s)]) != ""
}
