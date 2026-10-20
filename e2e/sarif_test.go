package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// Scenario 8 (design 0005): opt-in SARIF ingestion under container execution.
// The check runs in the container and writes its report into the /workspace
// mount; warden reads it on the host. The expected findings are the fixture's
// own, written here independently of warden, and each case asserts the
// artifacts (the feedback document, the problems output, the prediction on
// disk, the absence of a note), not just exit codes.

// sarifFixture is a report whose first result uses the container's absolute
// path, the second a repo-relative one, and the third a path outside the
// repository that must be dropped.
const sarifFixture = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"fixture"}},"results":[
 {"ruleId":"E100","level":"error","message":{"text":"unchecked error"},
  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"file:///workspace/src/app.go"},"region":{"startLine":7}}}]},
 {"ruleId":"W200","level":"warning","message":{"text":"shadowed variable"},
  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/lib.go"},"region":{"startLine":3}}}]},
 {"ruleId":"X","level":"error","message":{"text":"outside"},
  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"file:///etc/passwd"},"region":{"startLine":1}}}]}
]}]}`

// sarifRepo is feedbackRepo with `diagnostics: { test: out/test.sarif }` and
// the fixture committed under a name the check copies from, so the declared
// file is always one the run itself wrote.
func sarifRepo(t *testing.T, image, engine, command string) *goldenRepo {
	t.Helper()
	g := newGoldenRepo(t)
	cfg := fmt.Sprintf(`hooks: { pre_commit: true }
execution: { image: %q, runtime: %q }
predictive_cache: [test]
steps: { pre_commit: [test], pre_push: [test] }
commands: { test: %q }
diagnostics: { test: out/test.sarif }
rules: []
notify: false
`, image, engine, command)
	writeText(t, filepath.Join(g.dir, ".warden.yaml"), cfg)
	writeText(t, filepath.Join(g.dir, "sample"), "initial")
	if err := os.MkdirAll(filepath.Join(g.dir, "fixtures"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(g.dir, "fixtures", "report.json"), sarifFixture)
	g.git("add", ".warden.yaml", "sample", "fixtures/report.json")
	g.git("commit", "--no-verify", "-m", "configure staged checks with diagnostics")
	// Stage a change so the watcher has something to evaluate.
	writeText(t, filepath.Join(g.dir, "sample"), "changed")
	g.git("add", "sample")
	return g
}

func watchOnce(t *testing.T, g *goldenRepo, keyDir string) string {
	t.Helper()
	out, _ := g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, "watch", "--staged", "--once")
	return out
}

func feedbackProblems(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command(wardenBin, "feedback", "status", "--format", "problems")
	cmd.Dir = dir
	cmd.Env = hookEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("feedback status --format problems: %v\n%s", err, out)
	}
	return string(out)
}

func sarifOnly(step domain.FeedbackStep) []domain.FeedbackFinding {
	var out []domain.FeedbackFinding
	for _, f := range step.Findings {
		if f.Source == domain.FindingSourceSARIF {
			out = append(out, f)
		}
	}
	return out
}

func noNote(t *testing.T, dir string) {
	t.Helper()
	note := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
	note.Dir = dir
	if err := note.Run(); err == nil {
		t.Fatal("a note exists after a failing check")
	}
}

func TestGoldenFleet_SARIFFindingsMatchTheReport(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := sarifRepo(t, image, engine, "mkdir -p out && cp fixtures/report.json out/test.sarif && echo 'two problems' && exit 1")
	keyDir := t.TempDir()
	out := watchOnce(t, g, keyDir)

	doc, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatalf("no status.json written\n%s", out)
	}
	step, ok := testStep(doc)
	if !ok || step.Status != domain.FeedbackStepFail {
		t.Fatalf("step: %+v\n%s", doc.Steps, out)
	}
	if len(step.Findings) == 0 || step.Findings[0].Source != domain.FindingSourceOutput || !strings.Contains(step.Findings[0].Message, "two problems") {
		t.Fatalf("the verbatim output must stay: %+v", step.Findings)
	}
	want := []domain.FeedbackFinding{
		{Finding: domain.Finding{Severity: domain.SeverityHigh, Message: "unchecked error", File: "src/app.go", Line: 7, Rule: "E100"}, Source: domain.FindingSourceSARIF},
		{Finding: domain.Finding{Severity: domain.SeverityMedium, Message: "shadowed variable", File: "src/lib.go", Line: 3, Rule: "W200"}, Source: domain.FindingSourceSARIF},
	}
	got := sarifOnly(step)
	if len(got) != len(want) {
		t.Fatalf("sarif findings %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !strings.Contains(step.Summary, "diagnostics: 2 findings from out/test.sarif; 1 result outside the repository dropped") {
		t.Errorf("summary %q", step.Summary)
	}

	problems := feedbackProblems(t, g.dir)
	for _, line := range []string{
		"src/app.go:7: error: [test/E100] unchecked error\n",
		"src/lib.go:3: warning: [test/W200] shadowed variable\n",
	} {
		if !strings.Contains(problems, line) {
			t.Errorf("problems output lacks %q:\n%s", line, problems)
		}
	}
	if strings.Contains(problems, "passwd") {
		t.Errorf("an out-of-tree result reached the problems output:\n%s", problems)
	}

	// The gate itself shows the same located findings, and stays a failure
	// that writes no note.
	gate, code := g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, "run", "pre-commit")
	if code == 0 {
		t.Fatalf("the gate passed a failing check: %s", gate)
	}
	if !strings.Contains(gate, "src/app.go:7 unchecked error") {
		t.Errorf("gate output lacks the located finding:\n%s", gate)
	}
	noNote(t, g.dir)
}

func TestGoldenFleet_SARIFIgnoredWithANote(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	// A valid report OUTSIDE the repository, on the host. The container cannot
	// see this path, but it can plant a symlink to it in the mount, and warden
	// reads on the host.
	outside := t.TempDir()
	writeText(t, filepath.Join(outside, "leak.sarif"), strings.ReplaceAll(sarifFixture, "unchecked error", "LEAKED"))
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(outside, "dir", "test.sarif"), strings.ReplaceAll(sarifFixture, "unchecked error", "LEAKED"))
	cases := []struct {
		name, command, note string
	}{
		{"symlinked file", "mkdir -p out && ln -s " + filepath.Join(outside, "leak.sarif") + " out/test.sarif; exit 1",
			"diagnostics: ignored out/test.sarif: out/test.sarif is a symlink"},
		{"symlinked directory", "ln -s " + filepath.Join(outside, "dir") + " out; exit 1",
			"diagnostics: ignored out/test.sarif: out is a symlink"},
		{"oversized", "mkdir -p out && head -c 5000000 /dev/zero | tr '\\000' ' ' > out/test.sarif; exit 1",
			"diagnostics: ignored out/test.sarif: larger than 4 MiB"},
		{"not written", "exit 1",
			"diagnostics: ignored out/test.sarif: the check did not write it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := sarifRepo(t, image, engine, tc.command)
			out := watchOnce(t, g, t.TempDir())
			doc, ok := readStatus(t, g.dir)
			if !ok {
				t.Fatalf("no status.json written\n%s", out)
			}
			step, ok := testStep(doc)
			if !ok || step.Status != domain.FeedbackStepFail || step.Blocker != domain.BlockerNone {
				t.Fatalf("the verdict must be the command's own: %+v\n%s", doc.Steps, out)
			}
			if got := sarifOnly(step); len(got) != 0 {
				t.Fatalf("read an ignored report: %+v", got)
			}
			if !strings.Contains(step.Summary, tc.note) {
				t.Fatalf("summary %q, want note %q", step.Summary, tc.note)
			}
			data, _ := os.ReadFile(statusFile(t, g.dir))
			if strings.Contains(string(data), "LEAKED") {
				t.Fatal("content from outside the repository reached the feedback document")
			}
		})
	}
}

// A declared path outside the repository is refused when the configuration
// loads, so it can never be read.
func TestGoldenFleet_SARIFOutOfTreePathIsAConfigError(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := sarifRepo(t, image, engine, "exit 1")
	cfgPath := filepath.Join(g.dir, ".warden.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, cfgPath, strings.Replace(string(data), "out/test.sarif", "../test.sarif", 1))
	out, code := g.warden("run", "pre-commit")
	if code == 0 || !strings.Contains(out, "diagnostics.test") {
		t.Fatalf("an out-of-tree diagnostics path loaded: %d %s", code, out)
	}
}

// Exit status is the only verdict: a passing check whose report lists errors
// passes, and its staged prediction is saved.
func TestGoldenFleet_SARIFOnAPassIsNotRead(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := sarifRepo(t, image, engine, "mkdir -p out && cp fixtures/report.json out/test.sarif")
	keyDir := t.TempDir()
	out := watchOnce(t, g, keyDir)
	doc, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatalf("no status.json written\n%s", out)
	}
	step, ok := testStep(doc)
	if !ok || step.Status != domain.FeedbackStepPass || !step.Prediction.Saved || len(step.Findings) != 0 {
		t.Fatalf("step: %+v\n%s", doc.Steps, out)
	}
	if strings.Contains(step.Summary, "diagnostics") {
		t.Fatalf("a pass mentioned its report: %q", step.Summary)
	}
	var prediction domain.Prediction
	if err := json.Unmarshal(onlyPrediction(t, filepath.Join(g.dir, ".git", "warden", "predictions")), &prediction); err != nil {
		t.Fatal(err)
	}
	if prediction.Contract.TreeSHA != doc.Snapshot.Tree {
		t.Fatalf("prediction tree %s, snapshot %s", prediction.Contract.TreeSHA, doc.Snapshot.Tree)
	}
	// And the gate reuses it.
	gate, code := g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, "run", "pre-commit")
	if code != 0 || !strings.Contains(gate, "matched signed staged prediction") {
		t.Fatalf("the gate did not reuse the pass: %d %s", code, gate)
	}
}
