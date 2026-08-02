package domain

import "testing"

// A commit whose note does not attest it is NOT verified.
//
// "verified" used to mean "has a note", so a commit `warden verify` refuses
// outright was reported as verified in the summary line — with only a
// parenthetical to say otherwise. That is the tool disagreeing with itself, on
// the one line most readers stop at.
func TestCounts_ADefectiveNoteIsNotVerified(t *testing.T) {
	sound := func(sha string) CommitStatus {
		return CommitStatus{SHA: sha, HasNote: true, ChainIntact: true}
	}
	r := AuditReport{Commits: []CommitStatus{
		sound("a"),
		{SHA: "b", HasNote: true, ChainIntact: false, NoteDefect: DefectUnbound},
		{SHA: "c"}, // no note at all
	}}

	verified, defective, unverified := r.Counts()
	if verified != 1 {
		t.Errorf("verified = %d, want 1: only the attesting note counts", verified)
	}
	if defective != 1 {
		t.Errorf("defective = %d, want 1", defective)
	}
	// unverified is "everything not verified", defective included. doctor gates
	// its exit code on this, and a repo whose notes no longer attest anything is
	// exactly a repo that should be flagged.
	if unverified != 2 {
		t.Errorf("unverified = %d, want 2 (the defective note AND the missing one)", unverified)
	}
}

// The three buckets must account for every commit exactly once, or the fleet
// rollup — which sums them — loses or double-counts commits. Same invariant as
// the commit-state partition, one level up.
func TestCounts_AccountForEveryCommit(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "a", HasNote: true, ChainIntact: true},
		{SHA: "b", HasNote: true},
		{SHA: "c"},
		{SHA: "d", CoveredBy: "a"},
		{SHA: "e", ReattestableFrom: "a"},
	}}
	verified, _, unverified := r.Counts()
	if verified+unverified != len(r.Commits) {
		t.Errorf("verified(%d) + unverified(%d) = %d, want %d",
			verified, unverified, verified+unverified, len(r.Commits))
	}
}

// An empty report is all zeroes rather than anything surprising — doctor runs on
// a freshly adopted repo before there is any history to judge.
func TestCounts_EmptyReport(t *testing.T) {
	v, d, u := AuditReport{}.Counts()
	if v != 0 || d != 0 || u != 0 {
		t.Errorf("empty report: verified=%d defective=%d unverified=%d, want all 0", v, d, u)
	}
}
