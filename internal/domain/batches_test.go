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
			name: "agents and checks batch (each isolated); only rebase is a barrier",
			pol:  ResolvedPolicy{Steps: full, Parallel: true},
			want: [][]StepName{
				{StepIntent}, // followed by the rebase barrier
				{StepRebase}, // rewrites history → barrier
				{StepReview, StepDocument, StepTest, StepLint}, // agents + checks, each in its own worktree
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
		StepRebase:     false, // rewrites history → barrier
		StepPush:       false, // terminal write-external gate
		StepLint:       false, // has an auto-fix budget → writes are kept → barrier
		"codegen":      false, // declared under `writes:` → barrier
		StepIntent:     true,  // agent — isolatable (own worktree, writes discarded)
		StepReview:     true,  // agent — isolatable
		StepDocument:   true,  // agent — isolatable
		"custom-agent": true,  // rule-assigned agent — isolatable
		StepTest:       true,  // read-only shell check
		"custom":       true,  // custom command, no budget/writes
	}
	for step, want := range cases {
		if got := p.Concurrent(step); got != want {
			t.Errorf("Concurrent(%s) = %v, want %v", step, got, want)
		}
	}
}

func TestResolvedPolicy_WritesTree(t *testing.T) {
	p := ResolvedPolicy{
		AutoFix:    map[StepName]int{StepLint: 2, "checked": 0},
		Agents:     map[StepName]string{"custom-agent": "claude"},
		WriteSteps: map[StepName]bool{"codegen": true},
	}
	// writesTree = does it mutate the tree at all (kept or discarded);
	// keepsWrites = must its writes be preserved (→ barrier).
	cases := []struct {
		step                    StepName
		writesTree, keepsWrites bool
	}{
		{StepRebase, true, true},      // history rewrite, kept
		{StepLint, true, true},        // positive auto-fix budget, kept
		{"codegen", true, true},       // declared under writes:, kept
		{StepIntent, true, false},     // agent: writes, but discarded (isolatable)
		{StepReview, true, false},     // agent
		{StepDocument, true, false},   // agent
		{"custom-agent", true, false}, // rule-assigned agent
		{StepTest, false, false},      // read-only check
		{StepPush, false, false},      // external, handled separately
		{"checked", false, false},     // zero budget, no agent
		{"plain", false, false},       // ordinary custom command
	}
	for _, c := range cases {
		if got := p.WritesTree(c.step); got != c.writesTree {
			t.Errorf("WritesTree(%s) = %v, want %v", c.step, got, c.writesTree)
		}
		if got := p.KeepsWrites(c.step); got != c.keepsWrites {
			t.Errorf("KeepsWrites(%s) = %v, want %v", c.step, got, c.keepsWrites)
		}
		// Concurrent is exactly "not push and not a keeps-writes barrier".
		if got, want := p.Concurrent(c.step), c.step != StepPush && !c.keepsWrites; got != want {
			t.Errorf("Concurrent(%s) = %v, want %v", c.step, got, want)
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
