package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	feedbackTree   = strings.Repeat("a", 40)
	feedbackBase   = strings.Repeat("b", 40)
	feedbackConfig = "sha256:" + strings.Repeat("c", 64)
	feedbackEnv    = "sha256:" + strings.Repeat("d", 64)
	feedbackTime   = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
)

// goldenFeedback is a document exercising every field of the v1 contract.
func goldenFeedback() FeedbackDocument {
	at := func(seconds int) *time.Time {
		t := feedbackTime.Add(time.Duration(seconds) * time.Second)
		return &t
	}
	return FeedbackDocument{
		Schema: FeedbackSchema,
		Producer: FeedbackProducer{
			WardenVersion: "0.38.0",
			PID:           4242,
			StartedAt:     feedbackTime,
			HeartbeatAt:   *at(30),
		},
		Mode:        FeedbackModeStaged,
		Worktree:    "/abs/checkout",
		State:       FeedbackComplete,
		Message:     "staged checks failed: lint failed",
		Snapshot:    NewFeedbackSnapshot(feedbackTree, feedbackBase, "feat/x", feedbackConfig, feedbackEnv),
		EvaluatedAt: at(25),
		Steps: []FeedbackStep{
			{
				Step:       "lint",
				Status:     FeedbackStepFail,
				Summary:    "lint failed",
				StartedAt:  at(10),
				FinishedAt: at(20),
				Environment: &ExecutionEnvironment{
					Runtime: "docker", Image: "team-checks:local", ImageID: "sha256:" + strings.Repeat("e", 64),
					Platform: "linux/arm64", Network: "none",
				},
				Findings: []FeedbackFinding{
					{Finding: Finding{Severity: SeverityHigh, Message: "main.go:3: unused variable"}, Source: FindingSourceOutput},
					{Finding: Finding{Severity: SeverityMedium, Message: "deprecated call", File: "cfg.go", Line: 7, Rule: "SA1019"}, Source: FindingSourceStep},
				},
				OutputTruncated: true,
			},
			{
				Step:       "test",
				Status:     FeedbackStepPass,
				Summary:    "test passed; staged prediction saved",
				StartedAt:  at(10),
				FinishedAt: at(22),
				Prediction: FeedbackPrediction{Saved: true, ExpiresAt: at(622)},
				Findings:   []FeedbackFinding{},
			},
			{Step: "typecheck", Status: FeedbackStepPending, Findings: []FeedbackFinding{}},
		},
	}
}

// The schema is a public contract: editors parse these field names. Any
// change to this golden file is a change to warden.feedback/v1 and needs a
// new schema version instead. Regenerate only for an additive change, with
// WARDEN_UPDATE_GOLDEN=1.
func TestFeedbackSchemaGolden(t *testing.T) {
	got, err := json.MarshalIndent(goldenFeedback(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "feedback_v1.golden.json")
	if os.Getenv("WARDEN_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("warden.feedback/v1 changed:\n%s\nwant:\n%s", got, want)
	}
	var back FeedbackDocument
	if err := json.Unmarshal(want, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("golden document invalid: %v", err)
	}
}

func TestFeedbackSnapshotID(t *testing.T) {
	id := FeedbackSnapshotID(feedbackTree, feedbackBase, "main", feedbackConfig)
	if !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("id %q", id)
	}
	for name, other := range map[string]string{
		"tree":   FeedbackSnapshotID(strings.Repeat("f", 40), feedbackBase, "main", feedbackConfig),
		"base":   FeedbackSnapshotID(feedbackTree, "", "main", feedbackConfig),
		"branch": FeedbackSnapshotID(feedbackTree, feedbackBase, "dev", feedbackConfig),
		"config": FeedbackSnapshotID(feedbackTree, feedbackBase, "main", "sha256:"+strings.Repeat("0", 64)),
	} {
		if other == id {
			t.Errorf("%s does not change the id", name)
		}
	}
	// The environment is recorded beside the id, never in it: a reader cannot
	// recompute it without probing an engine.
	a := NewFeedbackSnapshot(feedbackTree, feedbackBase, "main", feedbackConfig, feedbackEnv)
	b := NewFeedbackSnapshot(feedbackTree, feedbackBase, "main", feedbackConfig, "")
	if a.ID != b.ID {
		t.Fatal("the environment digest changed the snapshot id")
	}
}

func TestFeedbackValidate(t *testing.T) {
	cases := map[string]func(*FeedbackDocument){
		"schema":            func(d *FeedbackDocument) { d.Schema = "warden.feedback/v2" },
		"mode":              func(d *FeedbackDocument) { d.Mode = "hook" },
		"pid":               func(d *FeedbackDocument) { d.Producer.PID = 0 },
		"heartbeat":         func(d *FeedbackDocument) { d.Producer.HeartbeatAt = time.Time{} },
		"state":             func(d *FeedbackDocument) { d.State = "passed" },
		"asserted current":  func(d *FeedbackDocument) { d.Freshness = FreshnessCurrent },
		"tree":              func(d *FeedbackDocument) { d.Snapshot.Tree = "HEAD" },
		"base":              func(d *FeedbackDocument) { d.Snapshot.Base = "main" },
		"config":            func(d *FeedbackDocument) { d.Snapshot.ConfigurationDigest = "" },
		"forged id":         func(d *FeedbackDocument) { d.Snapshot.Tree = strings.Repeat("9", 40) },
		"step name":         func(d *FeedbackDocument) { d.Steps[0].Step = "" },
		"step status":       func(d *FeedbackDocument) { d.Steps[0].Status = "green" },
		"finding source":    func(d *FeedbackDocument) { d.Steps[0].Findings[0].Source = "regex" },
		"duplicate step":    func(d *FeedbackDocument) { d.Steps[1].Step = d.Steps[0].Step },
		"unknown live mode": func(d *FeedbackDocument) { d.Mode = "" },
	}
	for name, mutate := range cases {
		doc := goldenFeedback()
		mutate(&doc)
		if err := doc.Validate(); !errors.Is(err, ErrInvalidFeedback) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
	doc := goldenFeedback()
	doc.Freshness = FreshnessStaleEnvironment
	doc.Mode = FeedbackModeLive
	doc.Snapshot = NewFeedbackSnapshot(feedbackTree, "", "main", feedbackConfig, "")
	if err := doc.Validate(); err != nil {
		t.Fatalf("producer-written stale_environment on an unborn branch rejected: %v", err)
	}
}

func TestFeedbackFreshness(t *testing.T) {
	now := feedbackTime.Add(time.Minute)
	expired := now.Add(-time.Second)
	live := FeedbackObservation{Now: now, ProducerAlive: true}
	cases := []struct {
		name   string
		mutate func(*FeedbackDocument, *FeedbackObservation)
		want   FeedbackFreshness
	}{
		{"current", func(*FeedbackDocument, *FeedbackObservation) {}, FreshnessCurrent},
		{"index moved", func(_ *FeedbackDocument, o *FeedbackObservation) { o.CurrentSnapshotID = "sha256:other" }, FreshnessStaleSnapshot},
		{"moved and gone: the snapshot wins", func(_ *FeedbackDocument, o *FeedbackObservation) {
			o.CurrentSnapshotID, o.ProducerAlive = "sha256:other", false
		}, FreshnessStaleSnapshot},
		{"pid gone", func(_ *FeedbackDocument, o *FeedbackObservation) { o.ProducerAlive = false }, FreshnessProducerGone},
		{"heartbeat old", func(d *FeedbackDocument, _ *FeedbackObservation) {
			d.Producer.HeartbeatAt = now.Add(-FeedbackProducerTimeout - time.Second)
		}, FreshnessProducerGone},
		{"stopped cleanly", func(d *FeedbackDocument, o *FeedbackObservation) {
			d.State, o.ProducerAlive = FeedbackStopped, false
			d.Producer.HeartbeatAt = now.Add(-time.Hour)
		}, FreshnessCurrent},
		{"environment moved", func(d *FeedbackDocument, _ *FeedbackObservation) { d.Freshness = FreshnessStaleEnvironment }, FreshnessStaleEnvironment},
		{"gone outranks environment", func(d *FeedbackDocument, o *FeedbackObservation) {
			d.Freshness, o.ProducerAlive = FreshnessStaleEnvironment, false
		}, FreshnessProducerGone},
		{"prediction expired", func(d *FeedbackDocument, _ *FeedbackObservation) { d.Steps[1].Prediction.ExpiresAt = &expired }, FreshnessExpired},
		{"cached prediction expired", func(d *FeedbackDocument, _ *FeedbackObservation) {
			d.Steps[1].Status, d.Steps[1].Prediction.ExpiresAt = FeedbackStepCached, &expired
		}, FreshnessExpired},
		{"a failing step has nothing to expire", func(d *FeedbackDocument, _ *FeedbackObservation) {
			d.Steps[0].Prediction.ExpiresAt = &expired
		}, FreshnessCurrent},
	}
	for _, tc := range cases {
		doc := goldenFeedback()
		doc.Producer.HeartbeatAt = now.Add(-time.Second)
		obs := live
		obs.CurrentSnapshotID = doc.Snapshot.ID
		tc.mutate(&doc, &obs)
		if got := doc.ComputeFreshness(obs); got != tc.want {
			t.Errorf("%s: freshness %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFeedbackStepFromResult(t *testing.T) {
	started, finished := feedbackTime, feedbackTime.Add(3*time.Second)
	saved := feedbackTime.Add(2 * time.Second)
	env := &ExecutionEnvironment{Runtime: "docker", ImageID: "sha256:" + strings.Repeat("e", 64)}

	pass := FeedbackStepFromResult(StepResult{Step: "test", Status: StepPass, Summary: "ok", Environment: env, PredictionSavedAt: saved}, started, finished)
	if pass.Status != FeedbackStepPass || !pass.Prediction.Saved || !pass.Prediction.ExpiresAt.Equal(saved.Add(PredictionTTL)) ||
		pass.Environment != env || !pass.StartedAt.Equal(started) || !pass.FinishedAt.Equal(finished) || pass.Findings == nil {
		t.Fatalf("pass: %+v", pass)
	}
	unsaved := FeedbackStepFromResult(StepResult{Step: "test", Status: StepPass}, time.Time{}, finished)
	if unsaved.Prediction.Saved || unsaved.Prediction.ExpiresAt != nil || unsaved.StartedAt != nil {
		t.Fatalf("unsaved pass: %+v", unsaved)
	}
	recorded := feedbackTime.Add(-time.Minute)
	cached := FeedbackStepFromResult(StepResult{Step: "test", Status: StepPass, Reuse: &ExecutionReuse{Source: PredictionPurpose, RecordedAt: recorded}}, started, finished)
	if cached.Status != FeedbackStepCached || cached.Prediction.Saved || !cached.Prediction.ExpiresAt.Equal(recorded.Add(PredictionTTL)) {
		t.Fatalf("cached: %+v", cached)
	}
	skipped := FeedbackStepFromResult(StepResult{Step: "test", Status: StepPass, Outcome: StepSkipped}, started, finished)
	if skipped.Status != FeedbackStepSkipped {
		t.Fatalf("skipped: %+v", skipped)
	}
	long := strings.Repeat("x", FeedbackMessageLimit-1) + "é" + "tail"
	failed := FeedbackStepFromResult(StepResult{
		Step: "lint", Status: StepFail, Blocker: BlockerContention,
		Findings: []Finding{
			{Severity: SeverityHigh, Message: long},
			{Severity: SeverityHigh, Message: "lock held", Rule: "step/lock-contention"},
			{Severity: SeverityLow, Message: "leak", File: "a.go", Line: 2, Rule: "SEC-1"},
			{Severity: SeverityLow, Message: "structured", Rule: "nox/AI-001"},
			{Severity: SeverityHigh, Message: "unchecked", File: "b.go", Line: 9, Rule: "errcheck", FromSARIF: true},
			{Severity: SeverityHigh, Message: "no location", FromSARIF: true},
		},
	}, started, finished)
	if failed.Status != FeedbackStepFail || failed.Blocker != BlockerContention || !failed.OutputTruncated {
		t.Fatalf("fail: %+v", failed)
	}
	if got := failed.Findings[0].Message; len(got) != FeedbackMessageLimit-1 || strings.Contains(got, "\xc3") {
		t.Fatalf("message cut %d bytes, mid-rune=%v", len(got), strings.Contains(got, "\xc3"))
	}
	wantSources := []FeedbackFindingSource{FindingSourceOutput, FindingSourceOutput, FindingSourceStep, FindingSourceStep, FindingSourceSARIF, FindingSourceSARIF}
	for i, want := range wantSources {
		if failed.Findings[i].Source != want {
			t.Errorf("finding %d source %q, want %q", i, failed.Findings[i].Source, want)
		}
	}
	approval := FeedbackStepFromResult(StepResult{Step: "lint", Status: StepNeedsApproval}, started, finished)
	if approval.Status != FeedbackStepFail {
		t.Fatalf("needs approval: %+v", approval)
	}
}
