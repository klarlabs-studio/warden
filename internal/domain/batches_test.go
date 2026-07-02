package domain

import (
	"reflect"
	"testing"
)

func TestResolvedPolicy_Batches(t *testing.T) {
	full := []StepName{StepIntent, StepRebase, StepReview, StepTest, StepDocument, StepLint}

	tests := []struct {
		name string
		pol  ResolvedPolicy
		want [][]StepName
	}{
		{
			name: "parallel groups consecutive read-only steps; rebase is a barrier",
			pol:  ResolvedPolicy{Steps: full, Parallel: true},
			want: [][]StepName{
				{StepIntent},
				{StepRebase},
				{StepReview, StepTest, StepDocument, StepLint},
			},
		},
		{
			name: "disabled parallelism keeps every step sequential",
			pol:  ResolvedPolicy{Steps: full, Parallel: false},
			want: [][]StepName{
				{StepIntent}, {StepRebase}, {StepReview}, {StepTest}, {StepDocument}, {StepLint},
			},
		},
		{
			name: "an auto-fix step is a barrier that splits the batch around it",
			pol: ResolvedPolicy{
				Steps:    []StepName{StepTest, StepLint, StepReview},
				Parallel: true,
				AutoFix:  map[StepName]int{StepLint: 2},
			},
			want: [][]StepName{{StepTest}, {StepLint}, {StepReview}},
		},
		{
			name: "custom command steps parallelize with built-ins",
			pol:  ResolvedPolicy{Steps: []StepName{StepTest, "security-scan", StepLint}, Parallel: true},
			want: [][]StepName{{StepTest, "security-scan", StepLint}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pol.Batches(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Batches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolvedPolicy_Concurrent(t *testing.T) {
	p := ResolvedPolicy{AutoFix: map[StepName]int{StepLint: 3}}
	cases := map[StepName]bool{
		StepRebase: false, // rewrites history
		StepPush:   false, // terminal write-external gate
		StepLint:   false, // has an auto-fix budget → writes
		StepTest:   true,  // read-only check
		StepReview: true,  // advisory agent
		"custom":   true,  // custom command, no budget
	}
	for step, want := range cases {
		if got := p.Concurrent(step); got != want {
			t.Errorf("Concurrent(%s) = %v, want %v", step, got, want)
		}
	}
}
