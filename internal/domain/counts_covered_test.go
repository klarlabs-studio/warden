package domain

import "testing"

// A fully-gated multi-commit push must not count as unverified.
//
// warden validates ONE tree per run and vouches for the span, so the
// intermediate commits of a gated push carry no note by design. doctor already
// prints them "✓ (covered by the gated push …)" and its own comment says
// reporting them as UNVERIFIED "was simply wrong" — but Counts still buckets
// them there, and doctor gates its exit code on exactly that number.
//
// This is the shape of a normal three-commit PR: commit, commit, commit, push.
func TestCounts_ASpanCoveredCommitIsNotUnverified(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "tip", HasNote: true, ChainIntact: true},
		{SHA: "mid", CoveredBy: "tip"},
		{SHA: "base", CoveredBy: "tip"},
	}}

	// Every commit passes the partition warden reports against.
	for i := range r.Commits {
		if r.Commits[i].Bypassed() {
			t.Fatalf("precondition: %s reads as bypassed", r.Commits[i].SHA)
		}
	}

	got := r.Counts()
	if got.Unverified != 0 {
		t.Errorf("unverified = %d, want 0: every commit was published by a gated push, "+
			"so `warden doctor` exits 1 on a correctly gated history", got.Unverified)
	}
	if got.Covered != 2 {
		t.Errorf("covered = %d, want 2", got.Covered)
	}
	if got.Verified != 1 {
		t.Errorf("verified = %d, want 1 (the tip carries the note)", got.Verified)
	}
	if got.Total() != len(r.Commits) {
		t.Errorf("Total() = %d, want %d: the buckets must account for every commit", got.Total(), len(r.Commits))
	}
}

// Same defect, one step earlier: a branch that was never pushed never reached
// the gate that writes notes. doctor prints "these are not bypasses" and then
// exits 1 anyway.
func TestCounts_AnUnpushedCommitIsNotUnverified(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "a", NoRemoteRef: true},
		{SHA: "b", NoRemoteRef: true},
	}}
	for i := range r.Commits {
		if !r.Commits[i].Unpushed() {
			t.Fatalf("precondition: %s does not read as unpushed", r.Commits[i].SHA)
		}
	}
	got := r.Counts()
	if got.Unverified != 0 {
		t.Errorf("unverified = %d, want 0: the pre-push gate never had an opportunity to run", got.Unverified)
	}
	if got.Unknown != 2 {
		t.Errorf("unknown = %d, want 2", got.Unknown)
	}
	if got.Total() != len(r.Commits) {
		t.Errorf("Total() = %d, want %d: the buckets must account for every commit", got.Total(), len(r.Commits))
	}
}

// A real gap still counts, or the fix would have turned the gate off rather
// than aimed it. Verifying the fix in the other direction is the point: a
// bypass, a squash-merge binding gap, and a note that does not attest its
// commit must all still land in Unverified.
func TestCounts_RealGapsStillCountAsUnverified(t *testing.T) {
	r := AuditReport{Commits: []CommitStatus{
		{SHA: "bypass"}, // no note, nothing vouches for it
		{SHA: "squashed", ReattestableFrom: "gated"}, // recoverable, but not yet bound
		{SHA: "broken", HasNote: true, ChainIntact: false, NoteDefect: DefectChainBroken},
	}}
	got := r.Counts()
	if got.Unverified != 3 {
		t.Errorf("unverified = %d, want 3 (bypass + reattestable + defective)", got.Unverified)
	}
	if got.Defective != 1 {
		t.Errorf("defective = %d, want 1", got.Defective)
	}
	if got.Verified != 0 || got.Covered != 0 || got.Unknown != 0 {
		t.Errorf("nothing should be excused: %+v", got)
	}
}
