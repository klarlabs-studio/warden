package application

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestDirtyValidatedTreeCannotPublish(t *testing.T) {
	for _, only := range []bool{false, true} {
		git := &fakeGit{root: t.TempDir(), branch: "feature", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1", diffSince: "changed tracked bytes"}, rewritesHistory: true}
		r := newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, prePushCfg())
		r.Settings.AttestOnly = only
		res, err := r.Run(context.Background(), domain.PrePush)
		if (err == nil && res.Outcome == domain.OutcomePassed) || git.pushed || git.wroteNote || git.notesPushed || res.PushPerformed {
			t.Fatalf("dirty validation published: error=%v result=%+v", err, res)
		}
	}
}

func TestPushTargetCannotChangeDuringSetup(t *testing.T) {
	for _, tc := range []struct{ branch, tip string }{{"other", "sha1"}, {"feature", "old-sha"}} {
		git := &fakeGit{root: t.TempDir(), branch: "feature", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
		r := newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, prePushCfg())
		r.Settings.ExpectedPushBranch = tc.branch
		r.Settings.ExpectedPushTip = tc.tip
		if _, err := r.Run(context.Background(), domain.PrePush); !errors.Is(err, ErrBranchMoved) {
			t.Fatalf("error=%v", err)
		}
		if git.pushed || git.wroteNote {
			t.Fatal("stale push target published")
		}
	}
}
