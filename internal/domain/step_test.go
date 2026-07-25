package domain

import "testing"

func TestDeferredSteps(t *testing.T) {
	tests := []struct {
		name  string
		ran   []StepName
		later []StepName
		want  []StepName
	}{
		{
			name:  "split policy defers the suite",
			ran:   []StepName{"lint"},
			later: []StepName{"test", "lint"},
			want:  []StepName{"test"},
		},
		{
			name:  "nothing deferred when the later hook is a subset",
			ran:   []StepName{"lint", "test"},
			later: []StepName{"lint"},
			want:  nil,
		},
		{
			name:  "identical lists defer nothing",
			ran:   []StepName{"lint", "test"},
			later: []StepName{"lint", "test"},
			want:  nil,
		},
		{
			name:  "order follows the later hook and duplicates collapse",
			ran:   []StepName{"lint"},
			later: []StepName{"rebase", "test", "test", "security-scan"},
			want:  []StepName{"rebase", "test", "security-scan"},
		},
		{
			name:  "an empty pre-commit defers everything",
			ran:   nil,
			later: []StepName{"lint", "test"},
			want:  []StepName{"lint", "test"},
		},
		{
			name:  "an empty later hook defers nothing",
			ran:   []StepName{"lint"},
			later: nil,
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeferredSteps(tc.ran, tc.later)
			if len(got) != len(tc.want) {
				t.Fatalf("DeferredSteps(%v, %v) = %v, want %v", tc.ran, tc.later, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("DeferredSteps(%v, %v) = %v, want %v", tc.ran, tc.later, got, tc.want)
				}
			}
		})
	}
}

// DeferredSteps must not mutate its inputs — callers reuse the resolved policy's
// step slice for the run record and the UI.
func TestDeferredSteps_DoesNotMutateInputs(t *testing.T) {
	ran := []StepName{"lint"}
	later := []StepName{"test", "lint"}
	DeferredSteps(ran, later)
	if len(ran) != 1 || ran[0] != "lint" {
		t.Errorf("ran mutated: %v", ran)
	}
	if len(later) != 2 || later[0] != "test" || later[1] != "lint" {
		t.Errorf("later mutated: %v", later)
	}
}

func TestJoinSteps(t *testing.T) {
	tests := []struct {
		in   []StepName
		want string
	}{
		{nil, ""},
		{[]StepName{"lint"}, "lint"},
		{[]StepName{"test", "lint"}, "test, lint"},
	}
	for _, tc := range tests {
		if got := JoinSteps(tc.in); got != tc.want {
			t.Errorf("JoinSteps(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
