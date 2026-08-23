package git

import (
	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Adapter wraps a *Repo to satisfy application.Git, bridging the git package's
// concrete methods to the port's vocabulary (e.g. staged-diff stats, worktree
// interfaces). Keeping the port thin here keeps the git package free of any
// application-layer types except the shared domain vocabulary.
type Adapter struct{ repo *Repo }

// NewAdapter returns an application.Git backed by repo.
func NewAdapter(repo *Repo) *Adapter { return &Adapter{repo: repo} }

// Root exposes the repo root so the Runner can locate .warden.yaml.
func (a *Adapter) Root() string { return a.repo.Dir }

func (a *Adapter) CurrentBranch() (string, error)       { return a.repo.CurrentBranch() }
func (a *Adapter) HeadSHA() (string, error)             { return a.repo.HeadSHA() }
func (a *Adapter) MergeBase(ref string) (string, error) { return a.repo.MergeBase(ref) }
func (a *Adapter) DiffStats(base string) (domain.DiffStats, error) {
	return a.repo.DiffStats(base)
}

// StagedDiffStats reports the index's change stats (the git package names this
// StagedPaths, but it returns full DiffStats).
func (a *Adapter) StagedDiffStats() (domain.DiffStats, error) { return a.repo.StagedPaths() }

// DefaultBranchRef exposes the remote's default head as a diff base.
func (a *Adapter) DefaultBranchRef(remote string) (string, error) {
	return a.repo.DefaultBranchRef(remote)
}

// EmptyTree exposes the empty-tree object as the last-resort diff base.
func (a *Adapter) EmptyTree() (string, error) { return a.repo.EmptyTree() }

// GitDir exposes the repository's .git directory to the application layer.
func (a *Adapter) GitDir() (string, error) { return a.repo.GitDir() }

func (a *Adapter) SeedWorktreeFromHead(materializeDeps bool) (application.Worktree, error) {
	wt, err := a.repo.CreateWorktreeFromHead(materializeDeps)
	if err != nil {
		return nil, err
	}
	return worktreeAdapter{wt}, nil
}

func (a *Adapter) SeedWorktreeFromBranch(branch string, materializeDeps bool) (application.Worktree, error) {
	wt, err := a.repo.CreateWorktreeFromBranch(branch, materializeDeps)
	if err != nil {
		return nil, err
	}
	return worktreeAdapter{wt}, nil
}

func (a *Adapter) FastForwardTo(branch, sha, expectedTip string) error {
	return a.repo.FastForwardTo(branch, sha, expectedTip)
}
func (a *Adapter) Push(remote, branch string, force domain.PushForce) error {
	return a.repo.Push(remote, branch, force)
}
func (a *Adapter) PushRewritesHistory(remote, branch string) (bool, error) {
	return a.repo.PushRewritesHistory(remote, branch)
}
func (a *Adapter) PushSpanBase(remote, branch string) (string, error) {
	return a.repo.PushSpanBase(remote, branch)
}
func (a *Adapter) UnmergedRemoteCommits(remote, branch string) ([]string, error) {
	return a.repo.UnmergedRemoteCommits(remote, branch)
}
func (a *Adapter) WriteNote(sha string, rec domain.RunRecord) error {
	return a.repo.WriteNote(sha, rec)
}
func (a *Adapter) AnchorAttested(sha string) error { return a.repo.AnchorAttested(sha) }
func (a *Adapter) PushNotes(remote string) error   { return a.repo.PushNotes(remote) }
func (a *Adapter) PushAnchor(remote, sha string) error {
	return a.repo.PushAnchor(remote, sha)
}

// worktreeAdapter adapts *Worktree to application.Worktree (Dir field → method).
type worktreeAdapter struct{ wt *Worktree }

func (w worktreeAdapter) Dir() string                { return w.wt.Dir }
func (w worktreeAdapter) HeadSHA() (string, error)   { return w.wt.HeadSHA() }
func (w worktreeAdapter) DiffSince() (string, error) { return w.wt.DiffSince() }
func (w worktreeAdapter) Remove() error              { return w.wt.Remove() }

func (w worktreeAdapter) Clone(materializeDeps bool) (application.Worktree, error) {
	clone, err := w.wt.Clone(materializeDeps)
	if err != nil {
		return nil, err
	}
	return worktreeAdapter{clone}, nil
}

// DetectDepDrift implements application.DepDriftDetector. The live checkout is
// supplied from the adapter's own repo, so callers never need to know that
// warden exposes dependencies from the working copy rather than reinstalling.
func (a *Adapter) DetectDepDrift(worktreeDir string) []domain.DepDrift {
	return DetectDepDrift(worktreeDir, a.repo.Dir)
}
