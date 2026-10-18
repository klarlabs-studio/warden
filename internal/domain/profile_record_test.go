package domain

import (
	"reflect"
	"testing"
)

func TestStepResult_ObservedOutcome(t *testing.T) {
	cases := []struct {
		name string
		res  StepResult
		want StepOutcome
	}{
		{"unset is executed", StepResult{}, StepExecuted},
		{"host cache hit", StepResult{Outcome: StepCached}, StepCached},
		{"skipped", StepResult{Outcome: StepSkipped}, StepSkipped},
		// Delivery folds a prediction into "cached"; the signed record keeps it
		// apart, because it rests on different evidence.
		{"staged prediction", StepResult{Reuse: &ExecutionReuse{Source: PredictionPurpose}}, StepPredicted},
		{"reuse of another kind stays cached", StepResult{Reuse: &ExecutionReuse{Source: "other"}}, StepCached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.ObservedOutcome(); got != tc.want {
				t.Errorf("ObservedOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// Only an agent that actually ran is named. A skipped or cached agent step
// carrying a stray Agent value must not have it recorded as the agent that
// performed the step.
func TestRun_StepOutcomesNameOnlyAnAgentThatRan(t *testing.T) {
	run := NewRun("run_1", PrePush, ResolvedPolicy{}, "main")
	for _, res := range []StepResult{
		{Step: StepLint, Status: StepPass},
		{Step: StepReview, Status: StepPass, Agent: "claude"},
		{Step: StepIntent, Status: StepPass, Outcome: StepSkipped, Agent: "claude"},
		{Step: StepDocument, Status: StepPass, Outcome: StepCached, Agent: "codex"},
	} {
		if err := run.RecordStep(res); err != nil {
			t.Fatal(err)
		}
	}
	want := map[StepName]StepObservation{
		StepLint:     {Outcome: StepExecuted},
		StepReview:   {Outcome: StepExecuted, Agent: "claude"},
		StepIntent:   {Outcome: StepSkipped},
		StepDocument: {Outcome: StepCached},
	}
	if got := run.StepOutcomes(); !reflect.DeepEqual(got, want) {
		t.Errorf("StepOutcomes = %+v\nwant %+v", got, want)
	}
	if got := NewRun("run_2", PrePush, ResolvedPolicy{}, "main").StepOutcomes(); got != nil {
		t.Errorf("a run with no steps observed nothing, got %+v", got)
	}
}

func TestProfileRef_RecordsTheBuiltInDefinition(t *testing.T) {
	p, err := ParseComplianceProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	ref := NewProfileRef(p)
	if ref.Name != "standard" || ref.Version != 1 || ref.Digest != p.Digest() || ref.String() != "standard@1" {
		t.Errorf("ref = %+v (%s)", ref, ref)
	}
}
