package application

import (
	"context"

	"go.klarlabs.de/warden/internal/domain"
)

// ConfigRepository loads the domain Config for the repository under gate.
// Infrastructure adapts the on-disk .warden.yaml to it, so the application
// layer never touches the filesystem or the YAML representation directly.
type ConfigRepository interface {
	Load() (domain.Config, error)
}

// Git is the repository port the Runner drives. Infrastructure adapts the git
// CLI to it; the Runner never shells out itself. All mutation happens in a
// Worktree, never the user's live checkout (§4.2/§4.3).
type Git interface {
	CurrentBranch() (string, error)
	HeadSHA() (string, error)
	// MergeBase returns the merge-base of HEAD and ref (e.g. "origin/main"),
	// used as the diff base for risk. Empty ref falls back to HEAD's parent.
	MergeBase(ref string) (string, error)
	// DiffStats summarizes base..HEAD; StagedDiffStats summarizes the index.
	DiffStats(base string) (domain.DiffStats, error)
	StagedDiffStats() (domain.DiffStats, error)

	SeedWorktreeFromHead() (Worktree, error)
	SeedWorktreeFromBranch(branch string) (Worktree, error)

	// FastForwardTo advances branch to sha only if branch still points at
	// expectedTip, else returns a branch-moved error (the mid-run guard).
	FastForwardTo(branch, sha, expectedTip string) error
	Push(remote, branch string) error
	WriteNote(sha string, rec domain.RunRecord) error
	PushNotes(remote string) error
}

// Worktree is a disposable checkout the pipeline mutates. Remove tears it down.
type Worktree interface {
	Dir() string
	// HeadSHA is the worktree's current commit, read after steps have run and
	// (for rebase/fix steps) committed their changes.
	HeadSHA() (string, error)
	// DiffSince returns a patch of everything changed in the worktree relative
	// to the commit it was seeded from — used to re-apply pre-commit fixes.
	DiffSince() (string, error)
	Remove() error
}

// StepOutcome is the Runner-facing result of executing one action through the
// kernel: the normalized step result, whether the kernel paused for approval,
// and the session id needed to resume it.
type StepOutcome struct {
	Result        domain.StepResult
	NeedsApproval bool
	SessionID     string
}

// Kernel is a per-run execution kernel (one axi kernel behind the port). The
// Runner executes steps through it and resolves the approval pause on the
// terminal push.
type Kernel interface {
	Execute(ctx context.Context, step domain.StepName) (StepOutcome, error)
	// ExecuteBatch runs steps concurrently and returns their outcomes in steps
	// order. onFinish, when non-nil, is called (concurrency-safe) as each step
	// completes so a live UI can show staggered progress. Evidence is folded into
	// the run's chain in steps order after all finish, so provenance stays
	// deterministic regardless of completion order.
	ExecuteBatch(ctx context.Context, steps []domain.StepName, onFinish func(domain.StepName, StepOutcome)) ([]StepOutcome, error)
	Approve(ctx context.Context, sessionID, principal, rationale string) (StepOutcome, error)
	Reject(ctx context.Context, sessionID, principal, rationale string) (StepOutcome, error)
	// Finalize verifies the aggregated run-level evidence chain and returns its
	// root hash and entries for the provenance note (§9).
	Finalize() (root string, entries []domain.EvidenceEntry, err error)
}

// PushFunc performs the fast-forward-back, origin push, and note write once the
// push action is approved. The Runner supplies it to the KernelFactory.
type PushFunc func(ctx context.Context) (domain.StepResult, error)

// KernelFactory builds a fresh Kernel for a single run.
type KernelFactory interface {
	New(policy domain.ResolvedPolicy, sc StepContext, priors *[]domain.Finding, push PushFunc) (Kernel, error)
}

// Forge is a code-hosting provider (GitHub via gh, …) used to open a pull
// request and read CI status after a passing push. It is optional: a nil Forge
// (or one that reports Available() == false) simply skips PR creation — the
// push and its provenance are unaffected.
type Forge interface {
	// Available reports whether the forge is usable (CLI installed, authed).
	Available() bool
	// EnsurePR opens a PR for branch onto base if none is open, else returns
	// the existing one. base "" means the forge's default branch. Idempotent.
	EnsurePR(ctx context.Context, branch, base string) (domain.PRInfo, error)
	// Comment posts (or updates) the warden gate-result comment on branch's PR.
	// It is sticky: repeated passing pushes update one comment rather than
	// stacking new ones.
	Comment(ctx context.Context, branch, body string) error
	// Checks returns the CI status for branch's pull request.
	Checks(ctx context.Context, branch string) (domain.CIStatus, error)
}

// ApprovalRequest is what the Runner shows an Approver when the run reaches its
// approval gate — the accumulated findings and the steps that produced them.
type ApprovalRequest struct {
	Hook     domain.Hook
	Branch   string
	Steps    []domain.StepName
	Findings []domain.Finding
	Risk     domain.Risk
}

// Decision is an approver's answer.
type Decision struct {
	Approved  bool
	Principal string
	Rationale string
}

// StepCache memoizes passing steps by their declared inputs so an unchanged
// step can be skipped on a later run (§4.4). It is optional: a nil cache simply
// runs every step. Implementations must be safe for concurrent use, since steps
// in a parallel batch consult it at once.
type StepCache interface {
	// Fingerprint hashes the contents of files under dir matching globs, or ""
	// when nothing matches (the step is then not cached this run).
	Fingerprint(dir string, globs []string) string
	// Seen reports whether key was recorded by a prior passing run.
	Seen(key string) bool
	// Record marks key as passed. Best-effort: a store failure is ignored.
	Record(key string)
}

// SBOM collects the dependency lockfiles present in a validated worktree, so a
// run record can carry a signed fingerprint of its dependency sets. It is
// optional: a nil SBOM leaves records without a dependency manifest.
type SBOM interface {
	Collect(dir string) []domain.DependencyManifest
}

// Signer produces detached ed25519 signatures over provenance payloads. It is
// optional: a nil Signer leaves run records unsigned, and a signing failure
// never fails a run — the note is still written, just without a signature (§9).
type Signer interface {
	// PublicKey is the base64 ed25519 public key that verifies this signer's
	// signatures. It is written into the record before signing so the key is
	// bound into its own signature.
	PublicKey() string
	// Sign returns a base64 ed25519 signature over payload.
	Sign(payload []byte) (string, error)
}

// Approver resolves the run's approval gate. Interactive delivery shows a TUI;
// agent surfaces answer programmatically; the default auto-approves clean runs.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// StepPhase marks a point in a step's lifecycle for progress reporting.
type StepPhase string

const (
	StepStarted  StepPhase = "started"
	StepFinished StepPhase = "finished"
	// StepOutput carries one line a running step emitted on stdout/stderr, for a
	// live output tail. These events are best-effort — a UI may drop them under
	// load — so they must never carry state the run depends on.
	StepOutput StepPhase = "output"
)

// StepEvent is emitted as the pipeline advances so a live UI can render
// progress. Result is meaningful only when Phase is StepFinished; Line only
// when Phase is StepOutput.
type StepEvent struct {
	Step   domain.StepName
	Phase  StepPhase
	Result domain.StepResult
	Line   string
}

// MultiObserver fans step events to several observers (e.g. the local TUI plus
// the attach socket). Nil elements are skipped.
type MultiObserver []Observer

// OnStep forwards to each non-nil observer.
func (m MultiObserver) OnStep(e StepEvent) {
	for _, o := range m {
		if o != nil {
			o.OnStep(e)
		}
	}
}

// Observer receives step lifecycle events during a run. It is optional; a nil
// Observer means no progress reporting (the non-interactive path). During a
// parallel batch, OnStep may be called concurrently from several goroutines, so
// an implementation must be safe under concurrent calls and must not block
// beyond a quick channel send.
type Observer interface {
	OnStep(StepEvent)
}
