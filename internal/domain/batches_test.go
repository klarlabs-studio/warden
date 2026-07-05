package domain

import (
	"reflect"
	"testing"
)

func TestResolvedPolicy_Batches(t *testing.T) {
	full := DefaultSteps(PrePush) // intent, rebase, review, document, test, lint

	tests := []struct {
		name string
		pol  ResolvedPolicy
		want [][]StepName
	}{
		{
			name: "only read-only checks batch; rebase and agent steps are barriers",
			pol:  ResolvedPolicy{Steps: full, Parallel: true},
			want: [][]StepName{
				{StepIntent},         // agent: may edit → barrier
				{StepRebase},         // rewrites history → barrier
				{StepReview},         // agent → barrier
				{StepDocument},       // agent → barrier
				{StepTest, StepLint}, // read-only checks → one parallel batch
			},
		},
		{
			name: "disabled parallelism keeps every step sequential",
			pol:  ResolvedPolicy{Steps: full, Parallel: false},
			want: [][]StepName{
				{StepIntent}, {StepRebase}, {StepReview}, {StepDocument}, {StepTest}, {StepLint},
			},
		},
		{
			name: "a config-declared writer is a barrier that splits reader batches",
			pol: ResolvedPolicy{
				Steps:      []StepName{StepTest, "codegen", StepLint},
				Parallel:   true,
				WriteSteps: map[StepName]bool{"codegen": true},
			},
			want: [][]StepName{{StepTest}, {"codegen"}, {StepLint}},
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
	p := ResolvedPolicy{
		AutoFix:    map[StepName]int{StepLint: 3},
		Agents:     map[StepName]string{"custom-agent": "claude"},
		WriteSteps: map[StepName]bool{"codegen": true},
	}
	cases := map[StepName]bool{
		StepRebase:     false, // rewrites history
		StepPush:       false, // terminal write-external gate
		StepLint:       false, // has an auto-fix budget → writes
		StepIntent:     false, // coding-agent step → may edit the tree
		StepReview:     false, // coding-agent step → may edit the tree
		StepDocument:   false, // coding-agent step → writes docs
		"custom-agent": false, // custom step a rule assigned an agent to
		"codegen":      false, // declared under `writes:`
		StepTest:       true,  // read-only shell check
		"custom":       true,  // custom command, no agent, no budget
	}
	for step, want := range cases {
		if got := p.Concurrent(step); got != want {
			t.Errorf("Concurrent(%s) = %v, want %v", step, got, want)
		}
	}
}

func TestResolvedPolicy_AuthorizesFix(t *testing.T) {
	cases := []struct {
		name    string
		autoFix map[StepName]int
		want    bool
	}{
		{"nil map", nil, false},
		{"empty map", map[StepName]int{}, false},
		{"zero budget only", map[StepName]int{StepLint: 0}, false},
		{"one positive budget", map[StepName]int{StepLint: 1}, true},
		{"mixed zero and positive", map[StepName]int{StepLint: 0, StepTest: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ResolvedPolicy{AutoFix: tc.autoFix}
			if got := p.AuthorizesFix(); got != tc.want {
				t.Errorf("AuthorizesFix() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolvedPolicy_Cacheable(t *testing.T) {
	p := ResolvedPolicy{
		Cache: map[StepName][]string{
			"test":   {"**/*.go"},
			"lint":   {"**/*.go"},
			"rebase": {"**/*.go"}, // declared, but rebase mutates → never cacheable
		},
		AutoFix: map[StepName]int{"lint": 1}, // auto-fix mutates → not cacheable
	}
	if !p.Cacheable("test") {
		t.Error("a read-only step with declared inputs must be cacheable")
	}
	if p.Cacheable("lint") {
		t.Error("an auto-fix step must not be cacheable")
	}
	if p.Cacheable("rebase") {
		t.Error("rebase mutates and must never be cacheable")
	}
	if p.Cacheable("review") {
		t.Error("a step without declared inputs must not be cacheable")
	}
	if got := p.CachePaths("test"); len(got) != 1 || got[0] != "**/*.go" {
		t.Errorf("CachePaths(test) = %v", got)
	}
}
