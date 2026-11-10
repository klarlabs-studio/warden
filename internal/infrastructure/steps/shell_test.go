package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		// Nothing examined the change, so a profiled run's signed record must
		// not say this step executed.
		if res.Outcome != domain.StepSkipped || res.OutcomeReason != "no command configured" {
			t.Errorf("an unconfigured shell step must be marked skipped, got %q (%q)", res.Outcome, res.OutcomeReason)
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
	// An ordinary status is a command that finished and said no. Calling it
	// "probably killed" sent the author of a `test -z "$(gofmt -l .)"` lint
	// looking for a timeout when the answer was an unformatted file.
	if !strings.Contains(f.Why, "ran to completion") {
		t.Errorf("why = %q, want it to say the command finished and rejected the change", f.Why)
	}
	if strings.Contains(f.Why, "killed") {
		t.Errorf("why = %q blames a kill for an ordinary exit status", f.Why)
	}
}

// The other silence: a command that did not get to finish. The exit status is
// what tells the two apart, so the explanation has to follow it.
func TestASilentKillIsNotCalledAVerdict(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"killed by a signal", "kill -KILL $$"},
		{"shell-reserved status", "exit 137"},
		{"command not runnable", "exit 126"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := NewShellStep(domain.StepLint, "lint")
			res, err := step.Run(context.Background(), application.StepContext{
				WorktreeDir: t.TempDir(),
				Commands:    map[string]string{"lint": tt.command},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != domain.StepFail || len(res.Findings) == 0 {
				t.Fatalf("status = %s with %d findings, want a failing finding", res.Status, len(res.Findings))
			}
			f := res.Findings[0]
			if f.Rule != "step/no-output" {
				t.Fatalf("rule = %q, want step/no-output", f.Rule)
			}
			if !strings.Contains(f.Why, "killed") {
				t.Errorf("why = %q, want the kill named as the likely cause", f.Why)
			}
			if strings.Contains(f.Why, "ran to completion") {
				t.Errorf("why = %q claims a verdict the command never reached", f.Why)
			}
		})
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

// The starter lint `warden init` writes for a Go project. gofmt -l exits 0
// whatever it finds, so the check has to fail on its output — and the usual
// way of doing that, `test -z "$(gofmt -l .)"`, swallows the file list. The
// step then failed having printed nothing, and the author of an unformatted
// file was told their command had probably been killed.
func TestGoStarterLintNamesTheFilesItRejects(t *testing.T) {
	for _, tool := range []string{"gofmt", "go", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	lint := domain.LanguageCommands(domain.LangGo)["lint"]
	const formatted = "package main\n\nfunc main() {}\n"

	tests := []struct {
		name       string
		files      map[string]string
		wantStatus domain.StepStatus
		wantNamed  []string
	}{
		{"formatted", map[string]string{"main.go": formatted}, domain.StepPass, nil},
		{
			"two unformatted files, one with a space in its name",
			map[string]string{
				"main.go":         formatted,
				"bad.go":          "package main\nfunc  a( ) {}\n",
				"sub/also bad.go": "package sub\nfunc  b( ) {}\n",
			},
			domain.StepFail,
			[]string{"bad.go", "sub/also bad.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.files["go.mod"] = "module example.test/starter\n\ngo 1.21\n"
			for name, content := range tt.files {
				full := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), application.StepContext{
				WorktreeDir: dir,
				Commands:    map[string]string{"lint": lint},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (findings: %+v)", res.Status, tt.wantStatus, res.Findings)
			}
			if tt.wantStatus == domain.StepPass {
				return
			}
			if len(res.Findings) == 0 {
				t.Fatal("a failed lint reported no finding")
			}
			f := res.Findings[0]
			if f.Rule == "step/no-output" {
				t.Errorf("the starter lint failed silently: %q", f.Message)
			}
			for _, name := range tt.wantNamed {
				if !strings.Contains(f.Message, name) {
					t.Errorf("message %q does not name %s", f.Message, name)
				}
			}
		})
	}
}
