package domain

import "testing"

// The three ways a commit can lack a note of its own, and only one of them is a
// bypass. Conflating them is what makes the metric untrustworthy: warden
// validates ONE tree per run, so the intermediate commits of a multi-commit push
// never get a note and never should — the span is the claim warden makes.
func TestCommitStatus_DistinguishesTheThreeGaps(t *testing.T) {
	cases := map[string]struct {
		c                               CommitStatus
		covered, reattestable, bypassed bool
	}{
		"has its own note": {
			CommitStatus{SHA: "a", HasNote: true}, false, false, false,
		},
		"published by a gated push span": {
			CommitStatus{SHA: "b", CoveredBy: "tip99"}, true, false, false,
		},
		"squash-merge, content was gated elsewhere": {
			CommitStatus{SHA: "c", ReattestableFrom: "pre99"}, false, true, false,
		},
		"genuinely went round the gate": {
			CommitStatus{SHA: "d"}, false, false, true,
		},
	}
	for name, tc := range cases {
		if got := tc.c.Covered(); got != tc.covered {
			t.Errorf("%s: Covered() = %v, want %v", name, got, tc.covered)
		}
		if got := tc.c.Reattestable(); got != tc.reattestable {
			t.Errorf("%s: Reattestable() = %v, want %v", name, got, tc.reattestable)
		}
		if got := tc.c.Bypassed(); got != tc.bypassed {
			t.Errorf("%s: Bypassed() = %v, want %v", name, got, tc.bypassed)
		}
	}
}

// A commit with its own note is never any kind of gap, whatever else is set on
// it — the note is the stronger claim.
func TestCommitStatus_ANotedCommitIsNeverAGap(t *testing.T) {
	c := CommitStatus{SHA: "a", HasNote: true, CoveredBy: "x", ReattestableFrom: "y"}
	if c.Covered() || c.Reattestable() || c.Bypassed() {
		t.Errorf("a noted commit must not be reported as any kind of gap: %+v", c)
	}
}

// The bypass count is the number meant to trigger an intervention. Counting a
// span-covered or reattestable commit inflates it, and a metric that overstates
// the problem gets dismissed as noisy — which costs more than not having one.
func TestAuditReport_BypassCountExcludesBothNonGaps(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "a", HasNote: true},
		{SHA: "b", CoveredBy: "a"},           // same push as a
		{SHA: "c", ReattestableFrom: "gone"}, // squash-merge
		{SHA: "d"},                           // the only real bypass
	}}
	n := 0
	for _, c := range r.Commits {
		if c.Bypassed() {
			n++
		}
	}
	if n != 1 {
		t.Errorf("bypassed = %d, want 1 (only the commit with no note, no span and no source)", n)
	}
}
