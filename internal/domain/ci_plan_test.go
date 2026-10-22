package domain

import "testing"

func TestCIJobPolicyValidation(t *testing.T) {
	for _, p := range []CIJobPolicy{
		{}, {Version: 2, Jobs: map[string]*ExecutionRequirements{"test": nil}},
		{Version: 1, Jobs: map[string]*ExecutionRequirements{"bad name": nil}},
		{Version: 1, Jobs: map[string]*ExecutionRequirements{"test": {Version: 2, Profile: &ProfileRequirement{Min: "strict"}}}},
		{Version: 1, Jobs: map[string]*ExecutionRequirements{"test": {Version: 1, Steps: map[StepName]StepRequirements{StepTest: {Command: "true"}}}}},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("accepted unsafe policy: %+v", p)
		}
	}
	if err := (CIJobPolicy{Version: 1, Jobs: map[string]*ExecutionRequirements{"always-run": nil}}).Validate(); err != nil {
		t.Fatal(err)
	}
}
