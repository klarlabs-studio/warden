package steps

import (
	"context"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestShellStep(t *testing.T) {
	ctx := context.Background()
	step := NewShellStep(domain.StepLint, "lint")

	t.Run("no command configured is an advisory pass", func(t *testing.T) {
		res, err := step.Run(ctx, application.StepContext{WorktreeDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass", res.Status)
		}
	})

	t.Run("zero exit passes", func(t *testing.T) {
		sc := application.StepContext{WorktreeDir: t.TempDir(), Commands: map[string]string{"lint": "true"}}
		res, err := step.Run(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass", res.Status)
		}
	})

	t.Run("exposes WARDEN_ env for incremental commands", func(t *testing.T) {
		sc := application.StepContext{
			WorktreeDir: t.TempDir(),
			Branch:      "feature/x",
			Diff:        domain.DiffStats{Paths: []string{"a.go", "b.go"}, FilesTouched: 2, LinesChanged: 9},
			Commands: map[string]string{"lint": `
				test "$WARDEN_BRANCH" = "feature/x" || exit 1
				test "$WARDEN_FILES_TOUCHED" = "2" || exit 1
				echo "$WARDEN_CHANGED_FILES" | grep -q a.go || exit 1
			`},
		}
		res, err := step.Run(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepPass {
			t.Errorf("WARDEN_ env not available to command: %+v", res)
		}
	})

	t.Run("non-zero exit fails with output finding", func(t *testing.T) {
		sc := application.StepContext{WorktreeDir: t.TempDir(), Commands: map[string]string{"lint": "echo boom >&2; exit 1"}}
		res, err := step.Run(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepFail {
			t.Fatalf("status = %s, want fail", res.Status)
		}
		if len(res.Findings) != 1 || res.Findings[0].Message != "boom" {
			t.Errorf("findings = %+v, want one 'boom'", res.Findings)
		}
	})
}
