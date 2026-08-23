package domain

import "testing"

// bound builds a chain-intact record naming commitSHA.
func boundTo(commitSHA string) RunRecord {
	return RunRecord{
		CommitSHA:         commitSHA,
		RunID:             "run-1",
		EvidenceChainRoot: "h0",
		Evidence:          []EvidenceEntry{{Hash: "h0"}},
	}
}

// The defect this splits apart. BindsTo is false in two different worlds and
// the old classifier returned DefectUnbound for both, so a note that named NO
// commit was reported as "bound to a different commit" — and the doctor label
// built on it said "history was rewritten" about history nobody had touched.
//
// Measured on this repository: all seven such notes were written by warden
// 0.8.3–0.9.0 between 4 and 6 July 2026, before records carried a commit id at
// all. Nothing was rebased.
func TestAttestDefect_SeparatesNoCommitFromWrongCommit(t *testing.T) {
	const sha = "abc123"

	// A note naming a DIFFERENT commit: the rebase/squash case, possibly
	// recoverable from a tree-identical commit.
	if got := boundTo("a-different-commit").AttestDefect(sha); got != DefectUnbound {
		t.Errorf("wrong-commit note = %q, want %q", got, DefectUnbound)
	}

	// A note naming NO commit: pre-binding, never recoverable.
	if got := boundTo("").AttestDefect(sha); got != DefectUnbindable {
		t.Errorf("no-commit note = %q, want %q", got, DefectUnbindable)
	}

	// And they must stay distinct, which is the whole point.
	if DefectUnbound == DefectUnbindable {
		t.Fatal("the two defects collapsed back into one")
	}
}

// Order matters: an empty CommitSHA also fails BindsTo, so the unbindable case
// has to be tested first or it falls through to the wrong-commit branch.
func TestAttestDefect_EmptyCommitIsCheckedBeforeMismatch(t *testing.T) {
	if got := boundTo("").AttestDefect("anything-at-all"); got == DefectUnbound {
		t.Error("an empty CommitSHA was classified as bound to a different commit — " +
			"the unbindable check must precede the mismatch check")
	}
}

// Neither is a pass. Naming a cause more precisely must not soften the verdict:
// both still fail Attests, and verify still refuses them.
func TestAttestDefect_NeitherFormAttests(t *testing.T) {
	const sha = "abc123"
	for name, rec := range map[string]RunRecord{
		"no commit":    boundTo(""),
		"wrong commit": boundTo("a-different-commit"),
	} {
		if rec.Attests(sha) {
			t.Errorf("%s: must not attest", name)
		}
	}
	// The bound one still does, so the new branch has not broken the good path.
	if !boundTo(sha).Attests(sha) {
		t.Error("a correctly bound record stopped attesting")
	}
}

// The count backing the evidence report's permanence note. It must track the
// unbindable population only — a wrong-commit note may yet be reattested, and
// counting it here would tell an auditor an open item is closed forever.
func TestPreBindingExceptions_CountsOnlyTheUnbindable(t *testing.T) {
	e := Evidence{Population: []CommitStatus{
		{SHA: "1", HasNote: true, NoteDefect: DefectUnbindable},
		{SHA: "2", HasNote: true, NoteDefect: DefectUnbindable},
		{SHA: "3", HasNote: true, NoteDefect: DefectUnbound},
		{SHA: "4", HasNote: true, NoteDefect: DefectChainBroken},
		{SHA: "5", HasNote: false},
		{SHA: "6", HasNote: true, ChainIntact: true},
	}}
	if got := e.PreBindingExceptions(); got != 2 {
		t.Errorf("PreBindingExceptions = %d, want 2", got)
	}
	if got := (Evidence{}).PreBindingExceptions(); got != 0 {
		t.Errorf("empty population = %d, want 0", got)
	}
}

// The reason string is what an auditor reads. It has to say the thing is
// permanent, because every other exception reason implies a to-do.
func TestExceptionReason_SaysUnbindableIsNotRecoverable(t *testing.T) {
	c := &CommitStatus{SHA: "1", HasNote: true, NoteDefect: DefectUnbindable}
	got := exceptionReason(c)
	for _, want := range []string{"predates commit binding", "not recoverable"} {
		if !contains(got, want) {
			t.Errorf("reason %q missing %q", got, want)
		}
	}
	// It must NOT reuse the generic wording, which reads as a rebase to fix.
	if contains(got, "does not attest the commit:") {
		t.Errorf("unbindable reused the generic defect wording: %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
