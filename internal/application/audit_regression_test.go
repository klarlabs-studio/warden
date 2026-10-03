package application

import (
	"context"
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
