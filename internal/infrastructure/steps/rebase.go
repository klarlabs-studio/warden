package steps

import (
	"context"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// RebaseStep rebases the worktree onto its upstream so conflicts surface inside
// the disposable worktree — never the developer's live checkout (§4.3). With no
// upstream configured it is an advisory pass. A conflict is aborted cleanly and
// reported as a finding rather than left half-applied.
type RebaseStep struct{}

// NewRebaseStep returns the rebase step.
func NewRebaseStep() RebaseStep { return RebaseStep{} }

func (RebaseStep) Name() domain.StepName { return domain.StepRebase }

func (RebaseStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	upstream, err := gitOut(ctx, sc.WorktreeDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || strings.TrimSpace(upstream) == "" {
		// No upstream to rebase onto (e.g. a brand-new branch): nothing to do.
		return domain.StepResult{Step: domain.StepRebase, Status: domain.StepPass, Summary: "rebase: no upstream, skipped"}, nil
	}

	if out, err := gitOut(ctx, sc.WorktreeDir, "rebase", strings.TrimSpace(upstream)); err != nil {
		// Abort so the worktree is left clean; the run fails and the developer
		// resolves the conflict before re-pushing.
		_, _ = gitOut(ctx, sc.WorktreeDir, "rebase", "--abort")
		return domain.StepResult{
			Step:   domain.StepRebase,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityHigh,
				Message:  "rebase onto " + strings.TrimSpace(upstream) + " failed: " + strings.TrimSpace(out),
			}},
			Summary: "rebase failed",
		}, nil
	}
	return domain.StepResult{Step: domain.StepRebase, Status: domain.StepPass, Summary: "rebased onto " + strings.TrimSpace(upstream)}, nil
}

// gitOut runs git in dir and returns combined output.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
