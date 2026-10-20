package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// failingWatchConfig declares the command on a line of its own, so the
// anchor line can be found in the text independently of warden.
const failingWatchConfig = `execution: {image: checks:local, runtime: docker}
predictive_cache: [test]
steps: {pre_commit: [test]}
commands:
  test: 'make test'
`

func feedbackFormat(t *testing.T, format string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdFeedback([]string{"status", "--format", format}, &out, &errb)
	return out.String(), errb.String(), code
}

// lineOf is the 1-based line of text that starts with prefix.
func lineOf(t *testing.T, text, prefix string) int {
	t.Helper()
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return i + 1
		}
	}
	t.Fatalf("no line starts with %q", prefix)
	return 0
}

type sarifOutput struct {
	Version string `json:"version"`
	Runs    []struct {
		Results []struct {
			Message    struct{ Text string } `json:"message"`
			Locations  []any                 `json:"locations"`
			Properties struct {
				Step    string `json:"step"`
				Source  string `json:"source"`
				Located bool   `json:"located"`
			} `json:"properties"`
		} `json:"results"`
		Properties struct {
			Warden struct {
				Freshness   string `json:"freshness"`
				Diagnostics bool   `json:"diagnostics"`
			} `json:"warden"`
		} `json:"properties"`
	} `json:"runs"`
}

func feedbackSARIF(t *testing.T) sarifOutput {
	t.Helper()
	out, errText, code := feedbackFormat(t, "sarif")
	if code != 0 {
		t.Fatalf("sarif: %d %s", code, errText)
	}
	var log sarifOutput
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("sarif is not JSON: %v\n%s", err, out)
	}
	if log.Version != "2.1.0" || len(log.Runs) != 1 {
		t.Fatalf("sarif: %s", out)
	}
	return log
}

// A real failing staged check, rendered for editors. The check prints a line
// shaped like a compiler diagnostic: warden must pass it through verbatim and
// never turn it into a location. The only file:line it prints is the anchor
// to the command's line in .warden.yaml, marked as such. Once the worktree
// moves, neither format may show the old results as current.
func TestFeedbackStatusRenderersOverARealEvaluation(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, failingWatchConfig)
	installFakeDocker(t, fakeDocker{failRun: true})
	stageFile(t, dir, "sample", "staged\n")
	var out, errb bytes.Buffer
	_ = cmdWatch([]string{"--staged", "--once"}, &out, &errb)
	if doc, errText, code := feedbackStatus(t); code != 0 || len(doc.Steps) != 1 || doc.Steps[0].Status != domain.FeedbackStepFail {
		t.Fatalf("the watcher did not record a failure: %d %s %+v\n%s%s", code, errText, doc.Steps, out.String(), errb.String())
	}

	problems, errText, code := feedbackFormat(t, "problems")
	if code != 0 {
		t.Fatalf("problems: %d %s", code, errText)
	}
	lines := strings.Split(strings.TrimSuffix(problems, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "warden: feedback stopped, current (staged, branch ") || !strings.HasSuffix(lines[0], "): 1 failed") {
		t.Fatalf("header: %q", lines[0])
	}
	anchor := fmt.Sprintf(".warden.yaml:%d: error: [test/config-anchor] test failed: ", lineOf(t, failingWatchConfig, "  test:"))
	var failed, anchored bool
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "warden: test failed: "):
			failed = true
		case strings.HasPrefix(line, anchor):
			anchored = true
		case strings.HasPrefix(line, "warden: "):
		default:
			t.Errorf("unexpected diagnostic %q", line)
		}
	}
	if !failed || !anchored {
		t.Fatalf("problems output:\n%s", problems)
	}

	log := feedbackSARIF(t)
	results := log.Runs[0].Results
	if len(results) != 1 || results[0].Properties.Step != "test" || results[0].Properties.Located || len(results[0].Locations) != 0 ||
		results[0].Properties.Source != string(domain.FindingSourceOutput) || !strings.Contains(results[0].Message.Text, "main.go:3: error: boom") {
		t.Fatalf("sarif results: %+v", results)
	}

	stageFile(t, dir, "sample", "restaged\n")
	problems, _, _ = feedbackFormat(t, "problems")
	for line := range strings.SplitSeq(strings.TrimSuffix(problems, "\n"), "\n") {
		if !strings.HasPrefix(line, "warden: ") {
			t.Errorf("stale document rendered a diagnostic: %q", line)
		}
	}
	if !strings.Contains(problems, "stale_snapshot") {
		t.Fatalf("staleness not stated:\n%s", problems)
	}
	log = feedbackSARIF(t)
	if len(log.Runs[0].Results) != 0 || log.Runs[0].Properties.Warden.Freshness != "stale_snapshot" || log.Runs[0].Properties.Warden.Diagnostics {
		t.Fatalf("stale sarif: %+v", log.Runs[0])
	}
}

func TestFeedbackRenderersWithoutADocument(t *testing.T) {
	repoWithConfig(t, "commands: { test: 'true' }\n")
	for _, format := range []string{"problems", "sarif"} {
		if out, errText, code := feedbackFormat(t, format); code != 1 || out != "" || !strings.Contains(errText, "warden watch --staged") {
			t.Errorf("%s: %d %q %q", format, code, out, errText)
		}
	}
}

// A configuration the anchors cannot be read from must not cost the user the
// failures: the renderer gets no anchors, and the reason goes to stderr.
func TestFeedbackProblemsWithoutAnchors(t *testing.T) {
	dir := repoWithConfig(t, failingWatchConfig)
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if got := commandAnchors(svc, &stderr); got["test"].Line != lineOf(t, failingWatchConfig, "  test:") {
		t.Fatalf("anchors: %+v %s", got, stderr.String())
	}
	// The anchor walk refuses an extends target outside the repository, as
	// the loader does.
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte("extends: ../outside.yaml\n"+failingWatchConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if got := commandAnchors(svc, &stderr); got != nil || !strings.Contains(stderr.String(), "not anchored") {
		t.Fatalf("anchors from a refused chain: %+v %s", got, stderr.String())
	}
}
