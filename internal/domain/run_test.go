package domain

import (
	"errors"
	"testing"
)

func newTestRun() *Run {
	p := ResolvedPolicy{Hook: PrePush, Steps: []StepName{"test", "lint"}}
	return NewRun("run_1", PrePush, p, "main")
}

func TestRun_PassingStepsThenPushed(t *testing.T) {
	run := newTestRun()
	if err := run.RecordStep(StepResult{Step: "test", Status: StepPass}); err != nil {
		t.Fatal(err)
	}
	if run.IsTerminal() {
		t.Fatal("run should still be pending after a passing step")
	}
	if err := run.MarkPushed(RunRecord{RunID: "run_1"}, "pushed"); err != nil {
		t.Fatal(err)
	}
	if run.Outcome() != OutcomePassed || run.Record() == nil {
		t.Errorf("expected passed with record, got %s / %v", run.Outcome(), run.Record())
	}
}

func TestRun_FailingStepTerminatesAndBlocksFurtherTransitions(t *testing.T) {
	run := newTestRun()
	if err := run.RecordStep(StepResult{Step: "lint", Status: StepFail,
		Findings: []Finding{{Severity: SeverityHigh, Message: "boom"}}}); err != nil {
		t.Fatal(err)
	}
	if run.Outcome() != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", run.Outcome())
	}
	// A terminal run rejects further recording and any push.
	if err := run.RecordStep(StepResult{Step: "test", Status: StepPass}); !errors.Is(err, ErrRunTerminal) {
		t.Errorf("recording past terminal should error with ErrRunTerminal, got %v", err)
	}
	if err := run.MarkPushed(RunRecord{}, "x"); !errors.Is(err, ErrRunTerminal) {
		t.Errorf("pushing a terminal run should error, got %v", err)
	}
}

func TestRun_RequiresApproval(t *testing.T) {
	cases := []struct {
		name   string
		policy ResolvedPolicy
		step   StepResult
		want   bool
	}{
		{"clean run", ResolvedPolicy{}, StepResult{Step: "lint", Status: StepPass}, false},
		{"rule requires it", ResolvedPolicy{RequireApproval: true}, StepResult{Step: "lint", Status: StepPass}, true},
		{"step asks for it", ResolvedPolicy{}, StepResult{Step: "review", Status: StepNeedsApproval}, true},
		{"high finding", ResolvedPolicy{}, StepResult{Step: "review", Status: StepPass,
			Findings: []Finding{{Severity: SeverityHigh, Message: "risky"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := NewRun("r", PrePush, c.policy, "main")
			if err := run.RecordStep(c.step); err != nil {
				t.Fatal(err)
			}
			if got := run.RequiresApproval(); got != c.want {
				t.Errorf("RequiresApproval() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRun_DeduplicatesFindings(t *testing.T) {
	run := newTestRun()
	f := Finding{Severity: SeverityLow, Message: "dup", File: "a.go", Line: 1}
	_ = run.RecordStep(StepResult{Step: "test", Status: StepPass, Findings: []Finding{f}})
	_ = run.RecordStep(StepResult{Step: "lint", Status: StepPass, Findings: []Finding{f}})
	if got := len(run.Findings()); got != 1 {
		t.Errorf("expected findings de-duplicated to 1, got %d", got)
	}
}

func TestRunRecord_VerifyChain(t *testing.T) {
	intact := RunRecord{
		EvidenceChainRoot: "h0",
		Evidence: []EvidenceEntry{
			{Hash: "h0"},
			{Hash: "h1", PreviousHash: "h0"},
			{Hash: "h2", PreviousHash: "h1"},
		},
	}
	if !intact.VerifyChain() {
		t.Error("well-formed chain should verify")
	}

	tampered := intact
	tampered.Evidence = append([]EvidenceEntry(nil), intact.Evidence...)
	tampered.Evidence[2].PreviousHash = "wrong"
	if tampered.VerifyChain() {
		t.Error("broken PreviousHash link must fail verification")
	}

	rerooted := intact
	rerooted.EvidenceChainRoot = "forged"
	if rerooted.VerifyChain() {
		t.Error("rewritten root must fail verification")
	}

	empty := RunRecord{}
	if !empty.VerifyChain() {
		t.Error("empty chain with empty root is vacuously intact")
	}
}
