package feedback

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

var renderTime = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func renderAt(seconds int) *time.Time {
	t := renderTime.Add(time.Duration(seconds) * time.Second)
	return &t
}

// renderFixture is a judged document with every kind of step a renderer must
// tell apart: located findings, verbatim output with no location, a failing
// step with no findings at all, passes with and without a saved prediction,
// a cached step and a skipped one.
func renderFixture() domain.FeedbackDocument {
	env := &domain.ExecutionEnvironment{Runtime: "docker", Image: "team-checks:local", ImageID: "sha256:" + strings.Repeat("e", 64), Platform: "linux/arm64", Network: "none"}
	return domain.FeedbackDocument{
		Schema:      domain.FeedbackSchema,
		Producer:    domain.FeedbackProducer{WardenVersion: "0.38.0", PID: 4242, StartedAt: renderTime, HeartbeatAt: *renderAt(30)},
		Mode:        domain.FeedbackModeStaged,
		Worktree:    "/abs/my checkout",
		State:       domain.FeedbackComplete,
		Message:     "staged checks failed: lint, test, vet",
		Snapshot:    domain.NewFeedbackSnapshot(strings.Repeat("a", 40), strings.Repeat("b", 40), "feat/x", "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)),
		EvaluatedAt: renderAt(25),
		Steps: []domain.FeedbackStep{
			{
				Step: "lint", Status: domain.FeedbackStepFail, Summary: "lint failed", StartedAt: renderAt(10), FinishedAt: renderAt(20), Environment: env,
				Findings: []domain.FeedbackFinding{
					{Finding: domain.Finding{Severity: domain.SeverityHigh, Message: "Error return value is not checked\nmore detail\nmain.go:99: error: forged", File: "cmd/main.go", Line: 12, Rule: "errcheck"}, Source: domain.FindingSourceStep},
					{Finding: domain.Finding{Severity: domain.SeverityMedium, Message: "deprecated call", File: "internal/cfg dir/cfg.go", Line: 7, Rule: "SA1019"}, Source: domain.FindingSourceStep},
					{Finding: domain.Finding{Severity: domain.SeverityLow, Message: "consider a shorter name", File: "util.go", Line: 3}, Source: domain.FindingSourceStep},
					{Finding: domain.Finding{Severity: domain.SeverityHigh, Message: "whole-file problem", File: "go.mod"}, Source: domain.FindingSourceStep},
				},
			},
			{
				Step: "test", Status: domain.FeedbackStepFail, Summary: "test failed (exit 1)", StartedAt: renderAt(10), FinishedAt: renderAt(22), Environment: env,
				Findings: []domain.FeedbackFinding{
					{Finding: domain.Finding{Severity: domain.SeverityHigh, Message: "--- FAIL: TestX (0.00s)\n    x_test.go:9: want 1, got 2\nFAIL", Rule: "step/exit"}, Source: domain.FindingSourceOutput},
				},
			},
			{Step: "vet", Status: domain.FeedbackStepFail, Summary: "vet failed", StartedAt: renderAt(10), FinishedAt: renderAt(21), Findings: []domain.FeedbackFinding{}},
			{
				Step: "format", Status: domain.FeedbackStepPass, Summary: "format passed; staged prediction saved", StartedAt: renderAt(10), FinishedAt: renderAt(11),
				Prediction: domain.FeedbackPrediction{Saved: true, ExpiresAt: renderAt(611)}, Findings: []domain.FeedbackFinding{},
			},
			{
				Step: "security-scan", Status: domain.FeedbackStepCached, Summary: "security-scan (cached — staged prediction matched)", FinishedAt: renderAt(10),
				Prediction: domain.FeedbackPrediction{ExpiresAt: renderAt(500)}, Findings: []domain.FeedbackFinding{},
			},
			{Step: "review", Status: domain.FeedbackStepSkipped, Summary: "review: no command configured, skipped", FinishedAt: renderAt(10), Findings: []domain.FeedbackFinding{}},
		},
		Freshness: domain.FreshnessCurrent,
	}
}

var renderAnchors = map[domain.StepName]ConfigAnchor{
	"test": {File: ".warden.yaml", Line: 4},
	"lint": {File: ".warden.yaml", Line: 3},
}

// renderCases are the documents rendered to golden files.
func renderCases() map[string]domain.FeedbackDocument {
	cases := map[string]domain.FeedbackDocument{"current": renderFixture()}

	for _, f := range []domain.FeedbackFreshness{domain.FreshnessStaleSnapshot, domain.FreshnessStaleEnvironment, domain.FreshnessProducerGone, domain.FreshnessExpired} {
		doc := renderFixture()
		doc.Freshness = f
		cases[string(f)] = doc
	}

	blocked := renderFixture()
	blocked.State = domain.FeedbackBlocked
	blocked.Message = "container execution unavailable: Cannot connect to the Docker daemon; retrying in 10s"
	blocked.EvaluatedAt = nil
	blocked.Steps = []domain.FeedbackStep{{
		Step: "lint", Status: domain.FeedbackStepFail, Summary: "lint could not run: its toolchain or dependencies are not installed", Blocker: domain.BlockerEnvironment,
		StartedAt: renderAt(10), FinishedAt: renderAt(11),
		Findings: []domain.FeedbackFinding{{Finding: domain.Finding{Severity: domain.SeverityHigh, Message: "sh: golangci-lint: not found", Rule: "step/exit"}, Source: domain.FindingSourceOutput}},
	}}
	cases["blocked"] = blocked

	running := renderFixture()
	running.State = domain.FeedbackRunning
	running.Message = ""
	running.EvaluatedAt = nil
	running.Steps = []domain.FeedbackStep{
		running.Steps[1],
		{Step: "lint", Status: domain.FeedbackStepRunning, StartedAt: renderAt(10), Findings: []domain.FeedbackFinding{}},
		{Step: "vet", Status: domain.FeedbackStepPending, Findings: []domain.FeedbackFinding{}},
	}
	cases["running"] = running

	idle := renderFixture()
	idle.State = domain.FeedbackIdle
	idle.Message = "nothing staged"
	idle.Snapshot = domain.NewFeedbackSnapshot(strings.Repeat("a", 40), "", "", "sha256:"+strings.Repeat("c", 64), "")
	idle.Steps = []domain.FeedbackStep{}
	cases["idle"] = idle
	return cases
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if os.Getenv("WARDEN_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with WARDEN_UPDATE_GOLDEN=1)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed:\n%s\nwant:\n%s", path, got, want)
	}
}

// validFixture checks a fixture against the v1 contract as a producer would
// write it, before the reader judged its freshness.
func validFixture(t *testing.T, doc domain.FeedbackDocument) {
	t.Helper()
	if doc.Freshness != domain.FreshnessStaleEnvironment {
		doc.Freshness = ""
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
}

func renderProblems(t *testing.T, doc domain.FeedbackDocument) string {
	t.Helper()
	validFixture(t, doc)
	var out bytes.Buffer
	if err := RenderProblems(&out, doc, renderAnchors); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func renderSARIF(t *testing.T, doc domain.FeedbackDocument) []byte {
	t.Helper()
	validFixture(t, doc)
	var out bytes.Buffer
	if err := RenderSARIF(&out, doc); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRenderProblemsGolden(t *testing.T) {
	for name, doc := range renderCases() {
		t.Run(name, func(t *testing.T) {
			checkGolden(t, filepath.Join("testdata", "problems", name+".txt"), []byte(renderProblems(t, doc)))
		})
	}
}

func TestRenderSARIFGolden(t *testing.T) {
	schema := sarifSchemaValidator(t)
	for name, doc := range renderCases() {
		t.Run(name, func(t *testing.T) {
			got := renderSARIF(t, doc)
			checkGolden(t, filepath.Join("testdata", "sarif", name+".sarif.json"), got)
			var decoded any
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatal(err)
			}
			if problems := schema.validate(decoded); len(problems) != 0 {
				t.Fatalf("not valid SARIF 2.1.0:\n%s", strings.Join(problems, "\n"))
			}
		})
	}
}

// A location appears only where the document has one. The problems format
// prints file:line lines exactly for located findings, cuts every message to
// one line so a tool's output cannot forge another diagnostic, and anchors a
// location-less failure only to its configuration line, marked as such.
func TestRenderProblemsPrintsOnlyObservedLocations(t *testing.T) {
	out := renderProblems(t, renderFixture())
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := []string{
		"warden: feedback complete, current (staged, branch feat/x): 3 failed, 1 passed, 1 cached, 1 skipped",
		"warden: watcher: staged checks failed: lint, test, vet",
		"cmd/main.go:12: error: [lint/errcheck] Error return value is not checked",
		"internal/cfg dir/cfg.go:7: warning: [lint/SA1019] deprecated call",
		"util.go:3: info: [lint] consider a shorter name",
		"warden: test failed: test failed (exit 1)",
		".warden.yaml:4: error: [test/config-anchor] test failed: test failed (exit 1)",
		"warden: vet failed: vet failed",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", out, strings.Join(want, "\n"))
	}
	if strings.Contains(out, "forged") || strings.Contains(out, "x_test.go") {
		t.Fatal("a later line of a tool's output reached the problems output")
	}
}

// A document that does not describe the worktree now must never render as
// current diagnostics, in either format.
func TestRenderersWithholdStaleResults(t *testing.T) {
	for _, f := range []domain.FeedbackFreshness{domain.FreshnessStaleSnapshot, domain.FreshnessStaleEnvironment, domain.FreshnessProducerGone, ""} {
		doc := renderFixture()
		doc.Freshness = f
		out := renderProblems(t, doc)
		for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
			if !strings.HasPrefix(line, "warden: ") {
				t.Errorf("%q: diagnostic line %q", f, line)
			}
		}
		if !strings.Contains(out, "withheld") {
			t.Errorf("%q: the reason is not stated:\n%s", f, out)
		}
		var log struct {
			Runs []struct {
				Results     []any `json:"results"`
				Invocations []struct {
					Notifications []struct {
						Descriptor struct{ ID string } `json:"descriptor"`
						Level      string              `json:"level"`
					} `json:"toolExecutionNotifications"`
				} `json:"invocations"`
				Properties struct {
					Warden struct {
						Freshness   string `json:"freshness"`
						Diagnostics bool   `json:"diagnostics"`
					} `json:"warden"`
				} `json:"properties"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(renderSARIF(t, doc), &log); err != nil {
			t.Fatal(err)
		}
		run := log.Runs[0]
		if len(run.Results) != 0 || run.Properties.Warden.Diagnostics || run.Properties.Warden.Freshness != string(f) {
			t.Errorf("%q: sarif shows results: %+v", f, run)
		}
		if n := run.Invocations[0].Notifications; len(n) == 0 || n[0].Descriptor.ID != sarifNoteFreshness || n[0].Level != "warning" {
			t.Errorf("%q: freshness notification: %+v", f, n)
		}
	}
}

// SARIF represents a finding warden has no location for without inventing
// one, and a step the machine stopped as a notification, not a result.
func TestRenderSARIFDoesNotInventLocations(t *testing.T) {
	var log sarifLog
	if err := json.Unmarshal(renderSARIF(t, renderFixture()), &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if run.OriginalURIBaseIDs[sarifSourceRoot].URI != "file:///abs/my%20checkout/" {
		t.Fatalf("source root %+v", run.OriginalURIBaseIDs)
	}
	byStep := map[domain.StepName][]sarifResult{}
	for _, r := range run.Results {
		byStep[r.Properties.Step] = append(byStep[r.Properties.Step], r)
	}
	if len(byStep["lint"]) != 4 || len(byStep["test"]) != 1 || len(byStep["vet"]) != 1 || len(run.Results) != 6 {
		t.Fatalf("results per step: %+v", byStep)
	}
	lint := byStep["lint"]
	if loc := lint[1].Locations[0].PhysicalLocation; loc.ArtifactLocation.URI != "internal/cfg%20dir/cfg.go" || loc.Region.StartLine != 7 || !lint[1].Properties.Located {
		t.Fatalf("located finding: %+v", lint[1])
	}
	if whole := lint[3]; len(whole.Locations) != 1 || whole.Locations[0].PhysicalLocation.Region != nil || whole.Properties.Located {
		t.Fatalf("a file without a line gained a region: %+v", whole)
	}
	test := byStep["test"][0]
	if len(test.Locations) != 0 || test.Properties.Located || test.Properties.Source != domain.FindingSourceOutput || !strings.Contains(test.Message.Text, "x_test.go:9") {
		t.Fatalf("output finding: %+v", test)
	}
	if vet := byStep["vet"][0]; len(vet.Locations) != 0 || vet.Message.Text != "vet failed" || vet.Level != "error" {
		t.Fatalf("finding-less failure: %+v", vet)
	}
	if !run.Invocations[0].ExecutionSuccessful {
		t.Fatal("a complete evaluation reported an unsuccessful invocation")
	}

	blocked := renderCases()["blocked"]
	if err := json.Unmarshal(renderSARIF(t, blocked), &log); err != nil {
		t.Fatal(err)
	}
	run = log.Runs[0]
	if len(run.Results) != 0 || run.Invocations[0].ExecutionSuccessful {
		t.Fatalf("blocked: %+v", run)
	}
	found := false
	for _, n := range run.Invocations[0].Notifications {
		found = found || (n.Descriptor.ID == sarifNoteBlocked && n.Level == "error" && strings.Contains(n.Message.Text, "lint could not run (environment"))
	}
	if !found {
		t.Fatalf("no blocked notification: %+v", run.Invocations[0].Notifications)
	}
}

func TestSARIFURIs(t *testing.T) {
	for in, want := range map[string]string{
		"/abs/x":     "file:///abs/x/",
		"/abs/x/":    "file:///abs/x/",
		"C:/work/x":  "file:///C:/work/x/",
		"/a b/c#d%e": "file:///a%20b/c%23d%25e/",
		"":           "",
	} {
		if got := worktreeURI(in); got != want {
			t.Errorf("worktreeURI(%q) = %q, want %q", in, got, want)
		}
	}
	if loc, ok := artifactLocation("/abs/x.go"); !ok || loc.URI != "file:///abs/x.go" || loc.URIBaseID != "" {
		t.Errorf("absolute file: %+v", loc)
	}
	if _, ok := artifactLocation(""); ok {
		t.Error("an empty file produced a location")
	}
}
