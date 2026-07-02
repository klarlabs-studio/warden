package domain

import "time"

// ResolvedPolicy is the effective configuration for one hook invocation, after
// all matching rules have been stacked (§5.2). It is what the kernel layer
// translates into axi-go Action registrations and budgets (§5.4), and what
// `warden policy explain` renders.
type ResolvedPolicy struct {
	Hook Hook
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
	Cache map[StepName][]string
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
// shared worktree. It must not rewrite history (rebase), must not be the
// terminal push, and must not apply auto-fixes — a non-zero budget writes to the
// tree. Read-only checks (lint, test, custom commands, advisory agents) qualify;
// anything that mutates the tree stays a sequential barrier.
func (p ResolvedPolicy) Concurrent(s StepName) bool {
	if s == StepRebase || s == StepPush {
		return false
	}
	return p.AutoFixBudget(s) == 0
}

// AgentFor returns the resolved agent for a step, or "" if the default applies.
func (p ResolvedPolicy) AgentFor(s StepName) string {
	if p.Agents == nil {
		return ""
	}
	return p.Agents[s]
}

// AutoFixBudget returns the retry budget for a step (0 = no auto-fix retries).
func (p ResolvedPolicy) AutoFixBudget(s StepName) int {
	if p.AutoFix == nil {
		return 0
	}
	return p.AutoFix[s]
}
