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
)

// StepEvent is emitted as the pipeline advances so a live UI can render
// progress. Result is meaningful only when Phase is StepFinished.
type StepEvent struct {
	Step   domain.StepName
	Phase  StepPhase
	Result domain.StepResult
}

// Observer receives step lifecycle events during a run. It is optional; a nil
// Observer means no progress reporting (the non-interactive path). Calls happen
// on the Runner's goroutine, so an implementation that feeds a UI must not
// block beyond a quick channel send.
type Observer interface {
	OnStep(StepEvent)
}
