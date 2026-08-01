package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/stepsdk"
)

// SubprocessStep is the trust boundary for repo-authored custom steps: they run
// as separate processes speaking the stepsdk wire protocol, never loaded into
// the daemon. It is also a published contract — anyone writing a
// `warden-step-*` binary depends on the exact JSON shape sent to stdin and the
// exact interpretation of what comes back.
//
// These tests run a real child process rather than stubbing exec, because the
// contract IS the process boundary: what lands on stdin, what a non-zero exit
// means, and what unparseable output means.

// fakeStep writes an executable that echoes a canned stdout, exits with a
// canned code, and records the stdin it was handed.
func writeFakeStep(t *testing.T, stdout string, exitCode int, stderr string) (bin, stdinLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake step is a shell script; warden is unix-first")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "warden-step-fake")
	stdinLog = filepath.Join(dir, "stdin.json")
	script := "#!/bin/sh\ncat > " + stdinLog + "\n"
	if stderr != "" {
		script += "printf '%s' " + shellQuote(stderr) + " >&2\n"
	}
	if stdout != "" {
		script += "printf '%s' " + shellQuote(stdout) + "\n"
	}
	script += "exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake step: %v", err)
	}
	return bin, stdinLog
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

func stepCtx(t *testing.T) application.StepContext {
	t.Helper()
	return application.StepContext{
		Hook:        domain.PrePush,
		WorktreeDir: t.TempDir(),
		Branch:      "feat/x",
		Agent:       "claude",
		Diff:        domain.DiffStats{FilesTouched: 3, LinesChanged: 42},
		PriorFindings: []domain.Finding{
			{Severity: domain.SeverityHigh, Message: "earlier problem", File: "a.go", Line: 7},
		},
	}
}

func TestSubprocessStepSendsTheDocumentedInput(t *testing.T) {
	out := `{"schema_version":1,"status":"pass"}`
	bin, stdinLog := writeFakeStep(t, out, 0, "")
	sc := stepCtx(t)

	if _, err := NewSubprocessStep("fake", bin).Run(context.Background(), sc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("step received no stdin: %v", err)
	}
	var got stepsdk.Input
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("step input was not valid stepsdk JSON: %v (%s)", err, raw)
	}

	want := stepsdk.Input{
		SchemaVersion: stepsdk.SchemaVersion,
		StepID:        "fake",
		Hook:          domain.PrePush.ConfigKey(),
		RepoPath:      sc.WorktreeDir,
		Branch:        "feat/x",
		DiffSummary:   stepsdk.DiffSummary{FilesTouched: 3, LinesChanged: 42},
		ResolvedAgent: "claude",
		PriorFindings: []stepsdk.Finding{
			{Severity: "high", Message: "earlier problem", File: "a.go", Line: 7},
		},
	}
	if got.SchemaVersion != want.SchemaVersion || got.StepID != want.StepID || got.Hook != want.Hook {
		t.Errorf("envelope = %+v, want schema=%d step=%s hook=%s", got, want.SchemaVersion, want.StepID, want.Hook)
	}
	if got.RepoPath != want.RepoPath || got.Branch != want.Branch || got.ResolvedAgent != want.ResolvedAgent {
		t.Errorf("context = repo:%q branch:%q agent:%q, want %q/%q/%q",
			got.RepoPath, got.Branch, got.ResolvedAgent, want.RepoPath, want.Branch, want.ResolvedAgent)
	}
	if got.DiffSummary != want.DiffSummary {
		t.Errorf("diff summary = %+v, want %+v", got.DiffSummary, want.DiffSummary)
	}
	if len(got.PriorFindings) != 1 || got.PriorFindings[0] != want.PriorFindings[0] {
		t.Errorf("prior findings = %+v, want %+v", got.PriorFindings, want.PriorFindings)
	}
}

func TestSubprocessStepRunsInTheWorktree(t *testing.T) {
	bin, _ := writeFakeStep(t, `{"schema_version":1,"status":"pass"}`, 0, "")
	sc := stepCtx(t)
	// Replace the script so it reports its working directory instead.
	marker := filepath.Join(t.TempDir(), "cwd")
	script := "#!/bin/sh\ncat > /dev/null\npwd > " + marker + "\nprintf '%s' '{\"schema_version\":1,\"status\":\"pass\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("rewrite fake step: %v", err)
	}

	if _, err := NewSubprocessStep("fake", bin).Run(context.Background(), sc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("step recorded no cwd: %v", err)
	}
	// A custom step inspects the change under review, so it must run in the
	// disposable worktree, not wherever the developer's shell happened to be.
	want, _ := filepath.EvalSymlinks(sc.WorktreeDir)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(b)))
	if got != want {
		t.Errorf("step ran in %q, want the worktree %q", got, want)
	}
}

func TestSubprocessStepTranslatesOutput(t *testing.T) {
	cases := []struct {
		name      string
		stdout    string
		wantState domain.StepStatus
		wantFixed bool
		wantFinds int
	}{
		{"pass", `{"schema_version":1,"status":"pass"}`, domain.StepPass, false, 0},
		{"fail", `{"schema_version":1,"status":"fail"}`, domain.StepStatus("fail"), false, 0},
		{
			"fixed with findings",
			`{"schema_version":1,"status":"pass","fixed":true,"findings":[{"severity":"medium","message":"reformatted","file":"x.go","line":3}]}`,
			domain.StepPass, true, 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, _ := writeFakeStep(t, c.stdout, 0, "")
			res, err := NewSubprocessStep("fake", bin).Run(context.Background(), stepCtx(t))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Step != domain.StepName("fake") {
				t.Errorf("Step = %q, want fake", res.Step)
			}
			if res.Status != c.wantState {
				t.Errorf("Status = %q, want %q", res.Status, c.wantState)
			}
			if res.Fixed != c.wantFixed {
				t.Errorf("Fixed = %v, want %v — the daemon re-stages on this flag", res.Fixed, c.wantFixed)
			}
			if len(res.Findings) != c.wantFinds {
				t.Fatalf("findings = %d, want %d", len(res.Findings), c.wantFinds)
			}
			if c.wantFinds > 0 {
				f := res.Findings[0]
				if f.Severity != domain.Severity("medium") || f.Message != "reformatted" || f.File != "x.go" || f.Line != 3 {
					t.Errorf("finding = %+v, want the wire values round-tripped", f)
				}
			}
		})
	}
}

// A non-zero exit is an OPERATIONAL failure of the step binary, which must be
// distinguishable from the step cleanly reporting StatusFail. Collapsing the
// two would let a crashing step read as a legitimate gate rejection.
func TestSubprocessStepNonZeroExitIsAnError(t *testing.T) {
	bin, _ := writeFakeStep(t, "", 3, "boom: could not start")
	res, err := NewSubprocessStep("fake", bin).Run(context.Background(), stepCtx(t))
	if err == nil {
		t.Fatalf("Run = nil error on a non-zero exit, got %+v", res)
	}
	if !strings.Contains(err.Error(), "boom: could not start") {
		t.Errorf("error = %v, want it to carry the child's stderr", err)
	}
	if res.Status != "" {
		t.Errorf("Status = %q on an operational error, want the zero value", res.Status)
	}
}

func TestSubprocessStepUnparseableOutputIsAnError(t *testing.T) {
	bin, _ := writeFakeStep(t, "not json at all", 0, "")
	if _, err := NewSubprocessStep("fake", bin).Run(context.Background(), stepCtx(t)); err == nil {
		t.Fatal("Run = nil error on unparseable stdout, want a decode error")
	} else if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error = %v, want a decode error", err)
	}
}

func TestSubprocessStepMissingBinaryIsAnError(t *testing.T) {
	if _, err := NewSubprocessStep("fake", filepath.Join(t.TempDir(), "nope")).
		Run(context.Background(), stepCtx(t)); err == nil {
		t.Fatal("Run = nil error for a missing binary, want an error")
	}
}

func TestSubprocessStepName(t *testing.T) {
	if got := NewSubprocessStep("lint-extra", "/bin/true").Name(); got != domain.StepName("lint-extra") {
		t.Errorf("Name() = %q, want lint-extra", got)
	}
}

// The convention is what makes a custom step discoverable without config: a
// step named "foo" resolves to "warden-step-foo" on PATH.
func TestCustomStepBinaryConvention(t *testing.T) {
	if got := customStepBinary("foo"); got != "warden-step-foo" {
		t.Errorf("customStepBinary(foo) = %q, want warden-step-foo", got)
	}
}

// nil rather than an empty slice, so `omitempty` drops the field and a step
// author can distinguish "no prior findings" from "the field is missing".
func TestToWireFindingsEmptyIsNil(t *testing.T) {
	if got := toWireFindings(nil); got != nil {
		t.Errorf("toWireFindings(nil) = %#v, want nil", got)
	}
	if got := toWireFindings([]domain.Finding{}); got != nil {
		t.Errorf("toWireFindings(empty) = %#v, want nil", got)
	}
}
