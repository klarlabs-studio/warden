// Package mcpserver exposes Warden's operation set as MCP tools (spec §4.6) so
// an AI agent can drive the same gate a human runs from the CLI.
//
// It depends only on the narrow Facade interface defined here, never on the
// concrete service: the CLI wires the real implementation into NewServer. That
// inversion keeps the MCP surface a thin, delivery-neutral adapter and lets the
// tool handlers be unit-tested against a fake without spinning up a pipeline.
package mcpserver

import (
	"context"
	"fmt"

	"go.klarlabs.de/mcp"

	"go.klarlabs.de/warden/internal/domain"
)

// Facade is the subset of Warden operations the MCP surface needs. The CLI
// wires a concrete implementation (the service) into NewServer.
type Facade interface {
	// PolicyExplain resolves effective policy for a hypothetical invocation.
	PolicyExplain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error)
	// StepsList returns built-in + configured step names grouped by hook.
	StepsList() (preCommit, prePush []domain.StepName, err error)
	// RunTrigger runs the pipeline for a hook and returns a compact summary.
	// Synchronous: the axi surface is a one-shot CLI invocation where blocking
	// until the verdict is exactly right.
	RunTrigger(ctx context.Context, hook domain.Hook) (RunSummary, error)
	// RunTriggerStreaming is RunTrigger with progress. onStep is called as each
	// step finishes, from the run's own goroutine, so it must be quick and
	// concurrency-safe. It backs the asynchronous MCP path, where a caller polls
	// rather than waits.
	RunTriggerStreaming(ctx context.Context, hook domain.Hook, onStep func(StepProgress)) (RunSummary, error)

	// The read-only provenance surface. These interrogate notes that already
	// exist; none of them runs repo-authored shell, so none is gated on trust.

	// Verify reports whether one commit carries trustworthy provenance.
	// trustedKeys, when non-empty, additionally requires a signature from one of
	// those keys.
	Verify(commitish string, trustedKeys []string) (ProvenanceRecord, error)
	// VerifyRange gates every commit in base..head.
	VerifyRange(base, head string, opts RangeVerifyRequest) (RangeVerifyOutput, error)
	// Doctor audits which commits since adoption carry a note.
	Doctor(branch string) (domain.AuditReport, error)
	// Audit exports the full commit-provenance report for compliance.
	Audit(branch string) (domain.AuditReport, error)
	// Status describes the gate's installed state.
	Status() (StatusOutput, error)
}

// ProvenanceRecord is a delivery-neutral verify result. It mirrors the service's
// VerifyResult rather than importing it: the MCP surface must not depend on the
// concrete service, or the inversion that keeps this package testable against a
// fake collapses.
type ProvenanceRecord struct {
	Validated      bool
	Signed         bool
	SignatureValid bool
	Signer         string
	Trusted        bool
	Record         *domain.RunRecord
}

// RangeVerifyRequest is the delivery-neutral option set for a range gate,
// mirroring the service's RangeVerifyOptions for the same reason.
type RangeVerifyRequest struct {
	RequireSigned bool
	TrustedKeys   []string
	// UseRoster resolves the trusted-signer roster from the range's BASE ref —
	// the trusted side — when no keys are pinned explicitly. A range gate must
	// never read trust from the head it is checking.
	UseRoster  bool
	SkipMerges bool
}

// RunSummary is a delivery-neutral run result the MCP tool returns.
type RunSummary struct {
	Outcome  string            `json:"outcome"` // passed|failed|rejected|aborted
	Hook     string            `json:"hook"`
	Steps    []domain.StepName `json:"steps"`
	Findings []domain.Finding  `json:"findings"`
	Message  string            `json:"message"`
	RunID    string            `json:"run_id,omitempty"`
	// Blocker names the environmental obstacle that ended a failed run — a
	// tool's lock, a missing toolchain — rather than the change itself; empty
	// means the verdict is about the change. Retryable is the actionable half:
	// contention clears on its own, a missing toolchain does not.
	//
	// The CLI has expressed this distinction since the exit-code split (75 vs
	// 78 vs 1), but the agent surfaces dropped it, leaving an agent to infer
	// "should I retry?" by parsing English prose. An agent that cannot tell
	// "your code is wrong" from "this machine wasn't ready" either retries a
	// real failure forever or gives up on a lock that would have cleared.
	Blocker   string `json:"blocker,omitempty"`
	Retryable bool   `json:"retryable"`
}

// PolicyExplainInput is the argument schema for the policy_explain tool. Branch
// and paths are optional so an agent can probe policy for a bare hook.
type PolicyExplainInput struct {
	Hook   string   `json:"hook" jsonschema:"required,description=Hook to resolve policy for: pre-commit or pre-push"`
	Branch string   `json:"branch,omitempty" jsonschema:"description=Branch name the invocation targets (optional)"`
	Paths  []string `json:"paths,omitempty" jsonschema:"description=Repo-relative paths touched, for path-glob rule matching (optional)"`
}

// StepsListInput takes no arguments; steps_list is a pure read of configuration.
type StepsListInput struct{}

// StepsListOutput groups step names by hook, matching the two hook points.
type StepsListOutput struct {
	PreCommit []domain.StepName `json:"pre_commit"`
	PrePush   []domain.StepName `json:"pre_push"`
}

// RunTriggerInput is the argument schema for the run_trigger tool.
type RunTriggerInput struct {
	Hook string `json:"hook" jsonschema:"required,description=Hook pipeline to run: pre-commit or pre-push"`
}

// VerifyInput asks whether one commit carries trustworthy provenance. Commit is
// optional so the common question — "is HEAD gated?" — needs no arguments.
type VerifyInput struct {
	Commit string `json:"commit,omitempty" jsonschema:"description=Commit-ish to verify (default HEAD)"`
	// TrustedKeys escalates the check: the note must additionally be signed by
	// one of these keys (full base64 public keys or fingerprints). Without it a
	// note is verified for chain integrity and binding but not for WHO signed.
	TrustedKeys []string `json:"trusted_keys,omitempty" jsonschema:"description=Require the note to be signed by one of these keys or fingerprints (optional)"`
}

// VerifyRangeInput gates every commit in base..head — the PR-check shape.
type VerifyRangeInput struct {
	Base string `json:"base" jsonschema:"required,description=Base ref of the range to gate e.g. origin/main"`
	Head string `json:"head,omitempty" jsonschema:"description=Head ref of the range (default HEAD)"`
	// RequireSigned and TrustedKeys escalate the gate from "provenance exists"
	// to "provenance I trust".
	RequireSigned bool     `json:"require_signed,omitempty" jsonschema:"description=Fail a commit whose note is unsigned"`
	TrustedKeys   []string `json:"trusted_keys,omitempty" jsonschema:"description=Require a signature from one of these keys or fingerprints (optional)"`
	// SkipMerges defaults true, matching the CLI: a merge commit's parents are
	// gated individually, so gating the merge itself double-counts.
	SkipMerges *bool `json:"skip_merges,omitempty" jsonschema:"description=Skip merge commits; their parents are gated individually (default true)"`
}

// RunStatusInput identifies the run to poll.
type RunStatusInput struct {
	RunID string `json:"run_id" jsonschema:"required,description=Run id returned by run_trigger"`
}

// BranchInput names a branch for the audit reads; empty means the current one.
type BranchInput struct {
	Branch string `json:"branch,omitempty" jsonschema:"description=Branch to report on (default the current branch)"`
}

// StatusInput takes no arguments; status is a pure read of installed state.
type StatusInput struct{}

// RunGate authorizes run_trigger before it executes the repository's configured
// commands. It returns nil to permit the run, or a descriptive error to refuse
// it. run_trigger runs on the auto-approving MCP/axi path with no human in the
// loop, so this is the sole operator-controlled checkpoint standing between an
// AI client and arbitrary shell authored by a possibly-untrusted cloned repo;
// the CLI supplies a gate backed by an explicit trust opt-in. The read-only
// tools (policy_explain, steps_list) never consult it. A nil gate leaves
// run_trigger unguarded, so callers embedding the server elsewhere must opt in
// deliberately.
type RunGate func() error

// AllowAllRuns is a permissive RunGate for trusted embeddings and tests.
func AllowAllRuns() error { return nil }

// runTriggerDescription documents that run_trigger executes repo-authored shell
// and is gated on explicit trust, so an agent reading the tool list understands
// the checkpoint before it calls and the refusal it may get back.
const runTriggerDescription = "Start the pipeline for a hook. Returns IMMEDIATELY with a run_id " +
	"and phase=running — poll run_status(run_id) for finished steps and the final summary, since " +
	"a full pipeline routinely takes minutes. " +
	"This EXECUTES the repository's configured commands as shell on the auto-approved, " +
	"non-interactive path (no human in the loop), so it is refused unless the operator has " +
	"explicitly trusted this repo (`warden trust add`, the axi --trust flag, or " +
	"WARDEN_MCP_ALLOW_RUN=1). The read-only tools are always available."

// NewServer builds an MCP server exposing Warden's operation set as typed tools:
//
//	policy_explain(hook, branch?, paths?)  -> ResolvedPolicy
//	steps_list()                           -> {pre_commit, pre_push}
//	run_trigger(hook)                      -> RunSummary
//	verify(commit?, trusted_keys?)         -> VerifyOutput
//	verify_range(base, head?, …)           -> RangeVerifyOutput
//	doctor(branch?)                        -> AuditOutput
//	audit(branch?)                         -> AuditOutput
//	status()                               -> StatusOutput
//
// Everything but run_trigger is a pure read and marked ReadOnly, so an agent can
// interrogate a repo's provenance — the question Warden exists to answer —
// without the trust opt-in that executing repo-authored shell requires.
//
// run_respond/run_status are intentionally absent: v0 runs synchronously, so
// there is no out-of-band run to poll or respond to. A stub tool documents this
// rather than silently omitting the operation, so an agent gets a clear error.
//
// gate authorizes run_trigger before it executes repo-authored commands; see
// RunGate. Pass AllowAllRuns for a permissive server.
func NewServer(f Facade, version string, gate RunGate) *mcp.Server {
	srv := mcp.NewServer(mcp.ServerInfo{
		Name:        "warden",
		Version:     version,
		Title:       "Warden",
		Description: "Git commit/push gate: verify commit provenance, explain policy, list steps, and run the pipeline.",
	})

	srv.Tool("policy_explain").
		Description("Resolve the effective policy for a hypothetical hook invocation.").
		ReadOnly().
		Handler(func(in PolicyExplainInput) (domain.ResolvedPolicy, error) {
			return handlePolicyExplain(f, in)
		})

	srv.Tool("steps_list").
		Description("List built-in and configured step names grouped by hook.").
		ReadOnly().
		Handler(func(StepsListInput) (StepsListOutput, error) {
			return handleStepsList(f)
		})

	srv.Tool("verify").
		Description("Report whether a commit carries trustworthy warden provenance: a signed, " +
			"hash-chained note bound to that exact commit, proving the configured checks ran and " +
			"passed. This is the provenance-skip primitive — CI can trust a validated commit and " +
			"skip re-running those checks. Fail-closed: an intact but unbound or transplanted note " +
			"is NOT validated.").
		ReadOnly().
		Handler(func(in VerifyInput) (VerifyOutput, error) {
			return handleVerify(f, in)
		})

	srv.Tool("verify_range").
		Description("Gate every commit in base..head, failing if any lacks trustworthy provenance. " +
			"Use this to answer 'is this whole branch/PR gated?'. Each commit gets a reason: ok, " +
			"missing, broken-chain, unsigned, or untrusted.").
		ReadOnly().
		Handler(func(in VerifyRangeInput) (RangeVerifyOutput, error) {
			return handleVerifyRange(f, in)
		})

	srv.Tool("doctor").
		Description("Audit which commits since warden was adopted carry a validation note — the " +
			"gaps where the gate was bypassed. Reports a reattestable count for gaps a " +
			"tree-identical validated commit can still vouch for (the squash-merge case).").
		ReadOnly().
		Handler(func(in BranchInput) (AuditOutput, error) {
			return handleDoctor(f, in)
		})

	srv.Tool("audit").
		Description("Export the full commit-provenance report for a branch, per commit, for " +
			"compliance reporting.").
		ReadOnly().
		Handler(func(in BranchInput) (AuditOutput, error) {
			return handleAudit(f, in)
		})

	srv.Tool("status").
		Description("Report the gate's installed state: which hooks are actually armed, the " +
			"adoption point, the steps that would run, and this machine's signing fingerprint. A " +
			"repo with a .warden.yaml but no installed hook is configured, not gated.").
		ReadOnly().
		Handler(func(StatusInput) (StatusOutput, error) {
			return f.Status()
		})

	runs := newRegistry()

	srv.Tool("run_trigger").
		Description(runTriggerDescription).
		Handler(func(_ context.Context, in RunTriggerInput) (RunStatusOutput, error) {
			return handleRunTrigger(f, gate, runs, in)
		})

	srv.Tool("run_status").
		Description("Poll a run started by run_trigger. Reports the steps that have finished so " +
			"far and, once the pipeline ends, the full summary. Phase is running, complete or " +
			"errored — note that 'complete' means the run finished, NOT that the gate passed; read " +
			"summary.outcome for the verdict.").
		ReadOnly().
		Handler(func(in RunStatusInput) (RunStatusOutput, error) {
			return handleRunStatus(runs, in)
		})

	srv.Tool("run_respond").
		Description("Not supported: this surface auto-approves, so a run never pauses for an " +
			"answer. The operator checkpoint here is the per-repo trust grant that permits " +
			"run_trigger at all, not a per-run prompt.").
		Handler(func(map[string]any) (struct{}, error) {
			return struct{}{}, errVisible(errNotSupported("run_respond"))
		})

	return srv
}

// Serve starts the server on stdio and blocks until ctx is canceled. gate
// authorizes run_trigger; see RunGate.
func Serve(ctx context.Context, f Facade, version string, gate RunGate) error {
	return mcp.ServeStdio(ctx, NewServer(f, version, gate))
}

// handlePolicyExplain parses the hook and delegates to the facade. It is split
// out of the tool closure so it can be unit-tested directly against a fake.
func handlePolicyExplain(f Facade, in PolicyExplainInput) (domain.ResolvedPolicy, error) {
	hook, err := domain.ParseHook(in.Hook)
	if err != nil {
		return domain.ResolvedPolicy{}, err
	}
	return f.PolicyExplain(hook, in.Branch, in.Paths)
}

// handleStepsList maps the facade's two-return-value shape onto the typed output
// struct so the tool marshals a stable, self-describing JSON object.
func handleStepsList(f Facade) (StepsListOutput, error) {
	preCommit, prePush, err := f.StepsList()
	if err != nil {
		return StepsListOutput{}, err
	}
	return StepsListOutput{PreCommit: preCommit, PrePush: prePush}, nil
}

// handleRunTrigger authorizes the run through the gate, then parses the hook and
// runs the pipeline, propagating context so the run honors cancellation from the
// MCP client. The gate is consulted before anything else so a refusal is
// deterministic and never leaks whether the hook or config was otherwise valid.
// A nil gate leaves the run unguarded (see RunGate).
func handleRunTrigger(f Facade, gate RunGate, runs *registry, in RunTriggerInput) (RunStatusOutput, error) {
	if gate != nil {
		if err := gate(); err != nil {
			// The refusal names the opt-in that resolves it, so it must survive
			// the dispatcher's sanitizing — see visible.
			return RunStatusOutput{}, errVisible(err)
		}
	}
	hook, err := domain.ParseHook(in.Hook)
	if err != nil {
		// Bad input: the model can retry with the right value, but only once it
		// can see what was wrong.
		return RunStatusOutput{}, errVisible(err)
	}
	// Returns as soon as the run is registered, not when it finishes. The
	// snapshot is therefore almost always phase=running with no steps yet —
	// what the caller needs from it is the run_id to poll.
	return runs.start(f, hook).snapshot(), nil
}

// handleRunStatus reports a run's progress. An unknown id is a visible error
// rather than an empty status: silently returning "no steps yet" for a run that
// does not exist would leave a caller polling a typo forever.
func handleRunStatus(runs *registry, in RunStatusInput) (RunStatusOutput, error) {
	if in.RunID == "" {
		return RunStatusOutput{}, errVisible(fmt.Errorf("run_status requires run_id (returned by run_trigger)"))
	}
	st := runs.lookup(in.RunID)
	if st == nil {
		return RunStatusOutput{}, errVisible(fmt.Errorf(
			"no run %q on this server — run ids are per-process and do not survive a restart", in.RunID))
	}
	return st.snapshot(), nil
}

// visible marks an error whose MESSAGE the caller is meant to read and act on,
// rather than one that merely reports that something went wrong.
//
// The dispatcher sanitizes a raw handler error to a bare "internal error" before
// it reaches the client — right for a failure that might leak paths or
// credentials, wrong for a refusal the caller is supposed to resolve: an agent
// told "internal error" cannot learn that the operator must trust this repo
// first, so it has no move except to give up or retry identically.
//
// Unwrap returns BOTH the cause and a *mcp.ToolInputError, because the two
// audiences need different things from the same error: the dispatcher finds the
// ToolInputError with errors.As and surfaces the message to the model, while
// callers (and tests) keep matching the original sentinel with errors.Is.
type visible struct{ cause error }

func (e *visible) Error() string { return e.cause.Error() }

func (e *visible) Unwrap() []error {
	return []error{e.cause, &mcp.ToolInputError{Message: e.cause.Error()}}
}

// errVisible wraps err so its message reaches the client. Returns nil for nil,
// so it can wrap a call site unconditionally.
func errVisible(err error) error {
	if err == nil {
		return nil
	}
	return &visible{cause: err}
}

// errNotSupported reports an operation that has no meaning in synchronous v0.
func errNotSupported(op string) error {
	return fmt.Errorf("%s is not supported: Warden v0 runs synchronously, so run_trigger returns the final outcome directly", op)
}
