package steps

import (
	"context"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// RebaseStep rebases the worktree onto the branch's INTEGRATION BASE so
// conflicts surface inside the disposable worktree — never the developer's live
// checkout (§4.3). A conflict is aborted cleanly and reported as a finding
// rather than left half-applied.
type RebaseStep struct{}

// NewRebaseStep returns the rebase step.
func NewRebaseStep() RebaseStep { return RebaseStep{} }

func (RebaseStep) Name() domain.StepName { return domain.StepRebase }

func (RebaseStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	base, why := resolveIntegrationBase(ctx, sc)
	if base == "" {
		return domain.StepResult{Step: domain.StepRebase, Status: domain.StepPass, Summary: "rebase: " + why + ", skipped"}, nil
	}

	if out, err := gitOut(ctx, sc.WorktreeDir, "rebase", base); err != nil {
		// Abort so the worktree is left clean; the run fails and the developer
		// resolves the conflict before re-pushing.
		_, _ = gitOut(ctx, sc.WorktreeDir, "rebase", "--abort")
		return domain.StepResult{
			Step:   domain.StepRebase,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityHigh,
				Message:  "rebase onto " + base + " failed: " + strings.TrimSpace(out),
			}},
			Summary: "rebase failed",
		}, nil
	}
	return domain.StepResult{Step: domain.StepRebase, Status: domain.StepPass, Summary: "rebased onto " + base}, nil
}

// resolveIntegrationBase names the ref this branch is meant to merge INTO, or
// "" plus a reason when there is nothing sensible to rebase onto.
//
// It must not be the branch's own remote-tracking ref. That was the original
// behavior (`@{upstream}`) and it is correct only while the branch has never
// been rewritten: after a local rebase onto an updated main — the standard way
// to satisfy "head branch is not up to date with the base branch" —
// `origin/<branch>` still holds the commit that was just replaced, so
// `origin/<branch>..HEAD` contains MAIN's commits. The step then replays main
// onto the superseded tip and fails on main's own conflicts, refusing a push
// that was never wrong (#102).
//
// Order matches the integration point becoming less certain: the PR base the
// repo configured, then the remote's default head, then an upstream that
// genuinely points somewhere else.
func resolveIntegrationBase(ctx context.Context, sc application.StepContext) (base, why string) {
	remote := sc.Remote
	if remote == "" {
		remote = "origin"
	}
	ownRef := remote + "/" + sc.Branch

	// 1. The repo said where PRs from here go; that is the integration point.
	if sc.PRBase != "" {
		ref := remote + "/" + strings.TrimSpace(sc.PRBase)
		if refExists(ctx, sc.WorktreeDir, ref) && ref != ownRef {
			return ref, ""
		}
	}
	// 2. The remote's default branch, e.g. origin/HEAD -> origin/main.
	if head, err := gitOut(ctx, sc.WorktreeDir, "rev-parse", "--abbrev-ref", remote+"/HEAD"); err == nil {
		if ref := strings.TrimSpace(head); ref != "" && ref != ownRef && refExists(ctx, sc.WorktreeDir, ref) {
			return ref, ""
		}
	}
	// 3. An explicitly-set upstream, but ONLY when it is not this branch's own
	//    remote ref — rebasing onto that is the bug this ordering exists to
	//    avoid, not a fallback.
	if up, err := gitOut(ctx, sc.WorktreeDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if ref := strings.TrimSpace(up); ref != "" && ref != ownRef {
			return ref, ""
		}
	}
	// Nothing but our own ref is available: a brand-new branch, or a repo whose
	// only branch is the one being pushed. Rebasing onto ourselves is a no-op at
	// best and the #102 failure at worst, so skip and say so.
	return "", "no integration base (only " + ownRef + ")"
}

// refExists reports whether ref resolves in dir.
func refExists(ctx context.Context, dir, ref string) bool {
	_, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// gitOut runs git in dir and returns combined output. The environment is
// scrubbed of git's hook variables: dir is the disposable worktree, and a
// GIT_INDEX_FILE inherited from the invoking hook points at the live checkout's
// index — relatively, so it does not even resolve from here (see scrubbedEnv).
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}
