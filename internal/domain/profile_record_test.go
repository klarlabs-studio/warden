package domain

import (
	"encoding/json"
	"reflect"
	"strings"
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

// The agent boundary reaches the record only as observed: isolation and the
// tree guard only when the step ran in a guarded clone (the guard's false is
// kept, it is an observation), the allowlist only when it was applied, and
// nothing for a step whose agent did not run now. Absence is never written as
// false.
func TestRun_StepOutcomesCarryOnlyObservedAgentBoundaries(t *testing.T) {
	yes, no := true, false
	run := NewRun("run_1", PrePush, ResolvedPolicy{}, "main")
	for _, res := range []StepResult{
		{Step: StepReview, Status: StepPass, Agent: "claude", AgentBoundary: &AgentBoundaryObservation{Isolated: true, TreeUnchanged: true, EnvAllowlisted: true}},
		{Step: StepIntent, Status: StepPass, Agent: "claude", AgentBoundary: &AgentBoundaryObservation{EnvAllowlisted: true}},
		{Step: "docs", Status: StepPass, Agent: "claude"},
		{Step: "summary", Status: StepPass, Outcome: StepCached, Agent: "claude", AgentBoundary: &AgentBoundaryObservation{Isolated: true, TreeUnchanged: true}},
		{Step: StepLint, Status: StepPass, AgentBoundary: &AgentBoundaryObservation{Isolated: true, TreeUnchanged: true}},
		// Last: a failed guard ends the run.
		{Step: StepDocument, Status: StepFail, Agent: "codex", AgentBoundary: &AgentBoundaryObservation{Isolated: true}},
	} {
		if err := run.RecordStep(res); err != nil {
			t.Fatal(err)
		}
	}
	want := map[StepName]StepObservation{
		StepReview:   {Outcome: StepExecuted, Agent: "claude", Isolated: &yes, TreeUnchanged: &yes, EnvAllowlisted: &yes},
		StepIntent:   {Outcome: StepExecuted, Agent: "claude", EnvAllowlisted: &yes},
		StepDocument: {Outcome: StepExecuted, Agent: "codex", Isolated: &yes, TreeUnchanged: &no},
		"docs":       {Outcome: StepExecuted, Agent: "claude"},
		"summary":    {Outcome: StepCached},
		StepLint:     {Outcome: StepExecuted},
	}
	got := run.StepOutcomes()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StepOutcomes = %s\nwant %s", mustJSON(t, got), mustJSON(t, want))
	}
	if j := mustJSON(t, got[StepIntent]); strings.Contains(j, "isolated") || strings.Contains(j, "tree_unchanged") {
		t.Errorf("an unisolated step must not mention isolation: %s", j)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
