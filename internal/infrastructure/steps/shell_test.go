package steps

import (
	"context"
	"strings"
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

// A command that fails without writing anything used to produce a finding with
// an empty message, an empty location and no rule, which printed as
//
//	[high]
//	warden: step lint failed
//
// naming neither what failed nor why. Observed when golangci-lint lost a race
// for its machine-global lock and exited without touching either stream: the
// commit was refused and the developer was given nothing to act on.
func TestASilentFailureSaysSo(t *testing.T) {
	ctx := context.Background()
	step := NewShellStep(domain.StepLint, "lint")
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		// exit 3, no output on either stream.
		Commands: map[string]string{"lint": "exit 3"},
	}

	res, err := step.Run(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if len(res.Findings) == 0 {
		t.Fatal("a failed step reported no finding at all")
	}

	f := res.Findings[0]
	if strings.TrimSpace(f.Message) == "" {
		t.Error("the finding carries no message, so the verdict prints as a bare severity tag")
	}
	// The exit status is the one fact available when the command said nothing:
	// 137 means it was killed, 3 means it ran and disagreed.
	if !strings.Contains(f.Message, "3") {
		t.Errorf("message %q does not say how the command exited", f.Message)
	}
	if !strings.Contains(f.Message, "printed nothing") {
		t.Errorf("message %q does not say the command was silent", f.Message)
	}
	// rule and why are the machine-readable and human-readable halves; a
	// silence that explains itself needs both.
	if f.Rule != "step/no-output" {
		t.Errorf("rule = %q, want step/no-output so this is classifiable", f.Rule)
	}
	if !strings.Contains(f.Why, "killed") {
		t.Errorf("why = %q, want the likely causes named", f.Why)
	}
}

// A command that fails WITH output keeps its own words: the new case must be
// the fallback, never a replacement for what the tool said.
func TestAFailureWithOutputKeepsIt(t *testing.T) {
	ctx := context.Background()
	step := NewShellStep(domain.StepLint, "lint")
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": "echo 'main.go:7: undefined: foo'; exit 1"},
	}

	res, err := step.Run(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no finding")
	}
	if !strings.Contains(res.Findings[0].Message, "undefined: foo") {
		t.Errorf("the tool's own output was lost: %q", res.Findings[0].Message)
	}
	if res.Findings[0].Rule == "step/no-output" {
		t.Error("a command that printed something was reported as silent")
	}
}
