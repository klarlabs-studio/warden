package domain

import "testing"

func TestHook(t *testing.T) {
	if !PreCommit.Valid() || !PrePush.Valid() || Hook("bogus").Valid() {
		t.Error("Valid() misclassified a hook")
	}
	if PreCommit.ConfigKey() != "pre_commit" || PrePush.ConfigKey() != "pre_push" {
		t.Error("ConfigKey mismatch")
	}
	for _, in := range []string{"pre-commit", "pre_commit"} {
		if h, err := ParseHook(in); err != nil || h != PreCommit {
			t.Errorf("ParseHook(%q) = %v, %v", in, h, err)
		}
	}
	for _, in := range []string{"pre-push", "pre_push"} {
		if h, err := ParseHook(in); err != nil || h != PrePush {
			t.Errorf("ParseHook(%q) = %v, %v", in, h, err)
		}
	}
	if _, err := ParseHook("commit-msg"); err == nil {
		t.Error("ParseHook should reject unknown hooks")
	}
}

func TestHookConfig_Enabled(t *testing.T) {
	hc := HookConfig{PreCommit: true, PrePush: false}
	if !hc.Enabled(PreCommit) || hc.Enabled(PrePush) || hc.Enabled(Hook("x")) {
		t.Errorf("Enabled misreported: %+v", hc)
	}
}

func TestStepName(t *testing.T) {
	for _, s := range []StepName{StepIntent, StepRebase, StepReview, StepTest, StepDocument, StepLint, StepPush} {
		if !s.IsBuiltin() {
			t.Errorf("%s should be built-in", s)
		}
	}
	if StepName("security-scan").IsBuiltin() {
		t.Error("custom step must not be built-in")
	}
	if got := DefaultSteps(PreCommit); len(got) != 1 || got[0] != StepLint {
		t.Errorf("pre-commit default = %v", got)
	}
	if got := DefaultSteps(PrePush); len(got) != 6 {
		t.Errorf("pre-push default = %v", got)
	}
	if DefaultSteps(Hook("x")) != nil {
		t.Error("unknown hook has no default steps")
	}
}

func TestRiskThresholds_Classify(t *testing.T) {
	th := DefaultRiskThresholds()
	if th.DiffLinesHigh != 400 || th.FilesTouchedHigh != 15 {
		t.Fatalf("defaults wrong: %+v", th)
	}
	cases := []struct {
		diff DiffStats
		want Risk
	}{
		{DiffStats{LinesChanged: 10, FilesTouched: 2}, RiskLow},
		{DiffStats{LinesChanged: 401}, RiskHigh},
		{DiffStats{FilesTouched: 16}, RiskHigh},
		{DiffStats{LinesChanged: 400, FilesTouched: 15}, RiskLow}, // boundary is exclusive
	}
	for _, c := range cases {
		if got := th.Classify(c.diff); got != c.want {
			t.Errorf("Classify(%+v) = %s, want %s", c.diff, got, c.want)
		}
	}
}

func TestRiskConfig_ThresholdsSubstitutesDefaults(t *testing.T) {
	// Zero fields fall back to documented defaults; set fields win.
	th := RiskConfig{DiffLinesHigh: 100}.Thresholds()
	if th.DiffLinesHigh != 100 || th.FilesTouchedHigh != 15 {
		t.Errorf("Thresholds = %+v", th)
	}
	def := RiskConfig{}.Thresholds()
	if def != DefaultRiskThresholds() {
		t.Errorf("empty config should equal defaults, got %+v", def)
	}
}

func TestMatch_Specificity(t *testing.T) {
	cases := []struct {
		m    Match
		want int
	}{
		{Match{}, 0},
		{Match{Branch: "main"}, 1},
		{Match{Branch: "main", Risk: RiskHigh}, 2},
		{Match{Branch: "main", Risk: RiskHigh, Paths: []string{"a"}}, 3},
	}
	for _, c := range cases {
		if got := c.m.Specificity(); got != c.want {
			t.Errorf("Specificity(%+v) = %d, want %d", c.m, got, c.want)
		}
	}
}

func TestResolvedPolicy_Accessors(t *testing.T) {
	p := ResolvedPolicy{
		Agents:  map[StepName]string{StepReview: "codex"},
		AutoFix: map[StepName]int{StepTest: 2},
	}
	if p.AgentFor(StepReview) != "codex" || p.AgentFor(StepLint) != "" {
		t.Error("AgentFor wrong")
	}
	if p.AutoFixBudget(StepTest) != 2 || p.AutoFixBudget(StepLint) != 0 {
		t.Error("AutoFixBudget wrong")
	}
	// Nil maps are safe.
	var empty ResolvedPolicy
	if empty.AgentFor(StepReview) != "" || empty.AutoFixBudget(StepTest) != 0 {
		t.Error("nil-map accessors must not panic and return zero")
	}
}

func TestNewRunID(t *testing.T) {
	if _, err := NewRunID(""); err == nil {
		t.Error("empty run id must be rejected")
	}
	if id, err := NewRunID("run_1"); err != nil || id != RunID("run_1") {
		t.Errorf("NewRunID = %v, %v", id, err)
	}
}

func TestNewCommitStatus(t *testing.T) {
	// No note → unverified.
	cs := NewCommitStatus("sha", "a", "d", "s", nil)
	if cs.HasNote || cs.ChainIntact {
		t.Error("nil note should be unverified")
	}
	// Note with intact chain.
	rec := &RunRecord{RunID: "r1", StepsRun: []StepName{StepLint},
		EvidenceChainRoot: "h0", Evidence: []EvidenceEntry{{Hash: "h0"}}}
	cs = NewCommitStatus("sha", "a", "d", "s", rec)
	if !cs.HasNote || !cs.ChainIntact || cs.RunID != "r1" {
		t.Errorf("verified commit misclassified: %+v", cs)
	}
}

func TestAuditReport_Counts(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{HasNote: true, ChainIntact: true},
		{HasNote: true, ChainIntact: false},
		{HasNote: false},
	}}
	v, i, u := r.Counts()
	if v != 2 || i != 1 || u != 1 {
		t.Errorf("Counts = %d,%d,%d want 2,1,1", v, i, u)
	}
}
