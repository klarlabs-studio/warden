package domain

import (
	"testing"
	"time"
)

func TestStepResult_ExecutionOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  StepResult
		want StepOutcome
	}{
		{"unset means executed", StepResult{Status: StepPass}, StepExecuted},
		{"skipped", StepResult{Status: StepPass, Outcome: StepSkipped}, StepSkipped},
		{"host cache", StepResult{Status: StepPass, Outcome: StepCached}, StepCached},
		{"prediction reuse is cached", StepResult{Status: StepPass, Reuse: &ExecutionReuse{RecordedAt: time.Unix(0, 0)}}, StepCached},
	} {
		if got := tc.res.ExecutionOutcome(); got != tc.want {
			t.Errorf("%s: ExecutionOutcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDescribeSteps(t *testing.T) {
	steps := []StepName{"lint", "test", "review"}
	if got, want := DescribeSteps(steps, nil), JoinSteps(steps); got != want {
		t.Errorf("with no notes DescribeSteps must equal JoinSteps: %q vs %q", got, want)
	}
	notes := map[StepName]OutcomeNote{
		"test":   {Outcome: StepCached, Reason: "inputs unchanged"},
		"review": {Outcome: StepSkipped, Reason: "no agent configured"},
	}
	want := "lint; test: cached, inputs unchanged; review: skipped, no agent configured"
	if got := DescribeSteps(steps, notes); got != want {
		t.Errorf("DescribeSteps = %q, want %q", got, want)
	}
	if got := (OutcomeNote{Outcome: StepSkipped}).String(); got != "skipped" {
		t.Errorf("reasonless note = %q", got)
	}
}

func TestRun_StepNotesListsOnlyStepsThatDidNotExecute(t *testing.T) {
	run := NewRun("run_1", PrePush, ResolvedPolicy{Steps: []StepName{"lint", "review"}}, "main")
	if err := run.RecordStep(StepResult{Step: "lint", Status: StepPass}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordStep(StepResult{Step: "review", Status: StepPass, Outcome: StepSkipped, OutcomeReason: "no agent configured"}); err != nil {
		t.Fatal(err)
	}
	notes := run.StepNotes()
	if len(notes) != 1 || notes["review"] != (OutcomeNote{Outcome: StepSkipped, Reason: "no agent configured"}) {
		t.Errorf("StepNotes = %+v", notes)
	}
	// The verdict is untouched: a skipped advisory step is still a pass.
	if err := run.Pass(); err != nil || run.Outcome() != OutcomePassed {
		t.Errorf("run with a skipped step must still pass: %v %s", err, run.Outcome())
	}
	if NewRun("run_2", PrePush, ResolvedPolicy{}, "main").StepNotes() != nil {
		t.Error("a run with no results has no notes")
	}
}
