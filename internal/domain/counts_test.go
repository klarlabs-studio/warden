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

	got := r.Counts()
	if got.Verified != 1 {
		t.Errorf("verified = %d, want 1: only the attesting note counts", got.Verified)
	}
	if got.Defective != 1 {
		t.Errorf("defective = %d, want 1", got.Defective)
	}
	// unverified includes defective. doctor gates its exit code on this, and a
	// repo whose notes no longer attest anything is exactly a repo that should
	// be flagged.
	if got.Unverified != 2 {
		t.Errorf("unverified = %d, want 2 (the defective note AND the missing one)", got.Unverified)
	}
}

// The buckets must account for every commit exactly once, or the fleet rollup —
// which sums them — loses or double-counts commits. Same invariant as the
// commit-state partition, one level up.
//
// Covered and Unknown are their own buckets rather than being swept into
// Unverified: every commit below the tip of a gated multi-commit push is
// Covered, and reporting those as unverified made doctor exit 1 on a history it
// had just printed all-✓.
func TestCounts_AccountForEveryCommit(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "a", HasNote: true, ChainIntact: true}, // verified
		{SHA: "b", HasNote: true},                    // defective ⊆ unverified
		{SHA: "c"},                                   // bypassed  ⊆ unverified
		{SHA: "d", CoveredBy: "a"},                   // covered
		{SHA: "e", ReattestableFrom: "a"},            // reattestable ⊆ unverified
		{SHA: "f", NoRemoteRef: true},                // unknown
		{SHA: "g", PreSpanProvenance: true},          // unknown
	}}
	got := r.Counts()
	if got.Total() != len(r.Commits) {
		t.Errorf("Total() = %d, want %d (%+v)", got.Total(), len(r.Commits), got)
	}
	if got.Verified != 1 || got.Covered != 1 || got.Unverified != 3 || got.Unknown != 2 {
		t.Errorf("Counts = %+v, want verified 1, covered 1, unverified 3, unknown 2", got)
	}
}

// An empty report is all zeroes rather than anything surprising — doctor runs on
// a freshly adopted repo before there is any history to judge.
func TestCounts_EmptyReport(t *testing.T) {
	if got := (AuditReport{}).Counts(); got != (Tally{}) {
		t.Errorf("empty report: %+v, want all 0", got)
	}
}
