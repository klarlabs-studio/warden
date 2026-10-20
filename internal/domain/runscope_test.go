package domain

import (
	"testing"
)

// affectedScope is a scope as a writer records it for an affected selection:
// two of three workspaces checked by one per-workspace step.
func affectedScope() *RunScope {
	return &RunScope{
		Version:             SelectionVersion,
		Kind:                SelectionAffected,
		Anchor:              "anchorsha",
		AnchorTree:          "anchortree",
		HeadTree:            "headtree",
		ConfigurationDigest: "sha256:config",
		GraphDigest:         "sha256:graph",
		SelectionDigest:     "sha256:selection",
		Workspaces:          []string{"apps/web", "packages/api", "packages/ui"},
		Selected: []SelectedWorkspace{
			{Path: "apps/web", Name: "web", Reasons: []SelectionReason{{Kind: ReasonDependent, Via: []string{"packages/ui"}}}},
			{Path: "packages/ui", Name: "ui", Reasons: []SelectionReason{{Kind: ReasonChanged, Paths: []string{"packages/ui/a.ts"}}}},
		},
		Steps: map[StepName]StepScope{
			"test": {Selected: []string{"apps/web", "packages/ui"}, TemplateDigest: "sha256:tmpl"},
		},
	}
}

// fullScope is the scope of a run that checked every workspace.
func fullScope() *RunScope {
	s := affectedScope()
	s.Kind = SelectionFull
	s.Anchor, s.AnchorTree = "", ""
	s.Full = &ForcedFull{Rule: FullRuleLockfile, Path: "pnpm-lock.yaml"}
	s.Steps = map[StepName]StepScope{"test": {Selected: s.Workspaces, TemplateDigest: "sha256:tmpl"}}
	return s
}

// Only a scope this warden can read as full is full. Every other shape,
// including one a newer warden might write, reads as partial.
func TestRunScope_IsFullFailsClosed(t *testing.T) {
	cases := map[string]struct {
		scope *RunScope
		full  bool
	}{
		"no scope (every note before monorepo mode)": {nil, true},
		"full":     {fullScope(), true},
		"affected": {affectedScope(), false},
		"unknown kind": {func() *RunScope {
			s := fullScope()
			s.Kind = "partial"
			return s
		}(), false},
		"empty kind": {func() *RunScope {
			s := fullScope()
			s.Kind = ""
			return s
		}(), false},
		"version zero": {func() *RunScope {
			s := fullScope()
			s.Version = 0
			return s
		}(), false},
		"newer selection version": {func() *RunScope {
			s := fullScope()
			s.Version = SelectionVersion + 1
			return s
		}(), false},
		"full but sparse": {func() *RunScope {
			s := fullScope()
			s.Sparse = &SparseScope{Cone: []string{"apps/web"}}
			return s
		}(), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.scope.IsFull(); got != tc.full {
				t.Errorf("IsFull = %v, want %v", got, tc.full)
			}
			rec := sampleRecord()
			rec.Scope = tc.scope
			if got := rec.FullScope(); got != tc.full {
				t.Errorf("FullScope = %v, want %v", got, tc.full)
			}
		})
	}
}

// A re-attestation whose own scope was removed still carries the affected
// original; it must not read as full under any policy, pinned key or not.
func TestRunRecord_FullScopeReadsTheCarriedOriginal(t *testing.T) {
	orig := sampleRecord()
	orig.Scope = affectedScope()
	outer := sampleRecord()
	outer.CommitSHA = "def456"
	outer.ReattestedFrom = orig.CommitSHA
	outer.CarriedOriginal = &orig
	if outer.FullScope() {
		t.Fatal("a re-attestation carrying an affected original read as full")
	}
	orig.Scope = fullScope()
	if !outer.FullScope() {
		t.Fatal("a re-attestation carrying a full original must read as full")
	}
}

func TestRunScope_Summary(t *testing.T) {
	var none *RunScope
	for _, tc := range []struct {
		name  string
		scope *RunScope
		want  string
	}{
		{"nil", none, ""},
		{"affected", affectedScope(), "affected: test[2/3 workspaces]"},
		{"full", fullScope(), "full: test[3/3 workspaces]"},
		{"unknown kind", &RunScope{Version: 1, Kind: "partial"}, `unknown kind "partial" (read as partial): 0/0 workspaces`},
		{"sparse full", &RunScope{Version: 1, Kind: SelectionFull, Workspaces: []string{"a"}, Selected: []SelectedWorkspace{{Path: "a"}}, Sparse: &SparseScope{Cone: []string{"a"}}},
			"full (read as partial by this warden): 1/1 workspaces, sparse checkout of 1 directories"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.Summary(); got != tc.want {
				t.Errorf("Summary = %q, want %q", got, tc.want)
			}
		})
	}
}
