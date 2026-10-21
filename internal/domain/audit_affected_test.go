package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func affectedNote(sha string) *RunRecord {
	return &RunRecord{CommitSHA: sha, EvidenceChainRoot: "r", Evidence: []EvidenceEntry{{Kind: "x", Hash: "r"}}, Scope: affectedScope()}
}

// An affected-scope note is gated but not verified, and so is every commit
// its push span covers: Counts puts them in their own bucket, which neither
// the verified count nor the gap count (doctor's exit code) includes.
func TestCounts_AffectedIsNeitherVerifiedNorAGap(t *testing.T) {
	full := NewCommitStatus("f", "", "", "", &RunRecord{CommitSHA: "f", EvidenceChainRoot: "r", Evidence: []EvidenceEntry{{Kind: "x", Hash: "r"}}, Scope: fullScope()})
	unscoped := NewCommitStatus("u", "", "", "", &RunRecord{CommitSHA: "u", EvidenceChainRoot: "r", Evidence: []EvidenceEntry{{Kind: "x", Hash: "r"}}})
	tip := NewCommitStatus("a", "", "", "", affectedNote("a"))
	span := NewCommitStatus("s", "", "", "", nil)
	span.CoveredBy = "a"
	span.MarkAffected(affectedNote("a"))
	fullSpan := NewCommitStatus("c", "", "", "", nil)
	fullSpan.CoveredBy = "f"
	defective := NewCommitStatus("d", "", "", "", affectedNote("elsewhere"))

	if !tip.Affected || tip.Scope != "affected 2/3" || full.Affected || unscoped.Affected || defective.Affected {
		t.Fatalf("affected marks: tip %v %q, full %v, unscoped %v, defective %v", tip.Affected, tip.Scope, full.Affected, unscoped.Affected, defective.Affected)
	}
	got := AuditReport{Commits: []CommitStatus{full, unscoped, tip, span, fullSpan, defective}}.Counts()
	want := Tally{Verified: 2, Covered: 1, Affected: 2, Defective: 1, Unverified: 1}
	if got != want {
		t.Fatalf("Counts = %+v, want %+v", got, want)
	}
	if got.Total() != 6 {
		t.Fatalf("Total = %d, want every commit once", got.Total())
	}
}

// The evidence digest of a population without an affected-scope commit is
// unchanged by the field's existence; with one, the scope is part of it.
func TestEvidenceDigest_AffectedOnlyWhenPresent(t *testing.T) {
	plain := Evidence{Population: []CommitStatus{{SHA: "a", HasNote: true, ChainIntact: true}}}
	before := plain.Digest()
	marked := Evidence{Population: []CommitStatus{{SHA: "a", HasNote: true, ChainIntact: true, Affected: true, Scope: "affected 1/2"}}}
	if marked.Digest() == before {
		t.Fatal("an affected commit must change the digest")
	}
	// Pinned: the row format from before scopes existed.
	sum := sha256.Sum256([]byte("a|true|true|||\n"))
	if before != hex.EncodeToString(sum[:]) {
		t.Fatalf("the digest of a population without affected commits changed: %s", before)
	}
	if len(marked.Exceptions()) != 0 {
		t.Fatal("an affected commit was gated: it is not an exception")
	}
}
