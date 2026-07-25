package service

import (
	"crypto/ed25519"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// spanRecord builds a record that attests tip and claims the push span
// (from, tip].
func spanRecord(from, tip, runID string) domain.RunRecord {
	rec := attestRecord(tip, runID)
	rec.CoversFrom = from
	return rec
}

// verdict returns the range result's verdict for sha.
func verdict(t *testing.T, res RangeVerifyResult, sha string) domain.CommitVerdict {
	t.Helper()
	for _, c := range res.Commits {
		if c.SHA == sha {
			return c
		}
	}
	t.Fatalf("commit %s not in range result %+v", sha, res.Commits)
	return domain.CommitVerdict{}
}

// The core of #86: warden notes only the tip of a push, so a plain
// commit-commit-commit-push leaves two commits with no note. The range gate
// must read the span the tip's note actually claims instead of demanding
// per-commit notes warden never writes.
func TestVerifyRange_SpanCoversTheRestOfThePush(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base (already on the remote)")
	a := commit(t, dir, svc, "A")
	b := commit(t, dir, svc, "B")
	tip := commit(t, dir, svc, "C (the push tip)")

	// One note, on the tip, claiming the span it published.
	if err := svc.Repo().WriteNote(tip, signAs(t, svc, spanRecord(base, tip, "r1"))); err != nil {
		t.Fatal(err)
	}

	res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("range should pass on a signed span: %+v", res.Failures())
	}
	for _, sha := range []string{a, b} {
		v := verdict(t, res, sha)
		if v.CoveredBy != tip {
			t.Errorf("%s CoveredBy = %q, want the push tip %s", sha, v.CoveredBy, tip)
		}
	}
	// The tip passed on its own note, not by coverage — the distinction has to
	// survive into the report.
	if v := verdict(t, res, tip); v.CoveredBy != "" {
		t.Errorf("the tip is individually attested; CoveredBy = %q", v.CoveredBy)
	}
}

// A span must not be a cheaper route to "verified": the covering note has to
// clear the very bar it is being used to satisfy.
func TestVerifyRange_UntrustedSpanCoversNothing(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	a := commit(t, dir, svc, "A")
	tip := commit(t, dir, svc, "B (tip)")

	// Validly signed, but by a key outside the roster.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().WriteNote(tip, sign(t, spanRecord(base, tip, "r1"), pub, priv)); err != nil {
		t.Fatal(err)
	}

	myFP, _ := svc.SigningKey()
	res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{
		RequireSigned: true,
		TrustedKeys:   []string{domain.KeyFingerprint(myFP)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("an untrusted note must not cover a span")
	}
	if v := verdict(t, res, a); v.CoveredBy != "" || v.Reason != domain.ReasonMissing {
		t.Errorf("A = %+v, want its original missing-note verdict", v)
	}
}

// An unsigned note must not cover a span under RequireSigned, even though it
// would satisfy the lenient default.
func TestVerifyRange_UnsignedSpanCoversNothingWhenSignatureRequired(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	a := commit(t, dir, svc, "A")
	tip := commit(t, dir, svc, "B (tip)")

	if err := svc.Repo().WriteNote(tip, spanRecord(base, tip, "r1")); err != nil { // unsigned
		t.Fatal(err)
	}

	strict, err := svc.VerifyRange(base, tip, RangeVerifyOptions{RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if strict.OK() {
		t.Fatal("an unsigned note must not cover a span when signatures are required")
	}
	if v := verdict(t, strict, a); v.CoveredBy != "" {
		t.Errorf("A was covered by an unsigned note: %+v", v)
	}

	// Under the lenient default the same note both attests and covers.
	lenient, err := svc.VerifyRange(base, tip, RangeVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !lenient.OK() {
		t.Fatalf("lenient gate should accept an attested span: %+v", lenient.Failures())
	}
}

// The span is bounded by real git history, not by what the note claims: a note
// naming a base it does not descend from covers nothing.
func TestVerifyRange_SpanCannotReachUnrelatedHistory(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	a := commit(t, dir, svc, "A")
	tip := commit(t, dir, svc, "B (tip)")

	// Claim a span starting at the tip itself: (tip, tip] is empty, so nothing is
	// covered however trusted the signer.
	if err := svc.Repo().WriteNote(tip, signAs(t, svc, spanRecord(tip, tip, "r1"))); err != nil {
		t.Fatal(err)
	}
	res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if v := verdict(t, res, a); v.CoveredBy != "" {
		t.Errorf("an empty span covered A: %+v", v)
	}
}

// A commit outside every span keeps its own verdict — coverage rescues only
// what a gated push actually published.
func TestVerifyRange_CommitOutsideEverySpanStillFails(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	outside := commit(t, dir, svc, "pushed with --no-verify")
	spanStart := commit(t, dir, svc, "start of the gated push")
	inside := commit(t, dir, svc, "inside the push")
	tip := commit(t, dir, svc, "tip")

	if err := svc.Repo().WriteNote(spanStart, signAs(t, svc, attestRecord(spanStart, "r0"))); err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().WriteNote(tip, signAs(t, svc, spanRecord(spanStart, tip, "r1"))); err != nil {
		t.Fatal(err)
	}

	res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("the --no-verify commit must still fail")
	}
	if v := verdict(t, res, outside); v.Reason != domain.ReasonMissing {
		t.Errorf("outside commit = %+v, want a missing-note failure", v)
	}
	if v := verdict(t, res, inside); v.CoveredBy != tip {
		t.Errorf("inside commit = %+v, want coverage by %s", v, tip)
	}
}

// A note with no span claim behaves exactly as before, so nothing about the
// single-commit path changes.
func TestVerifyRange_NoSpanClaimIsUnchanged(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	a := commit(t, dir, svc, "A")
	tip := commit(t, dir, svc, "B (tip)")

	if err := svc.Repo().WriteNote(tip, signAs(t, svc, attestRecord(tip, "r1"))); err != nil {
		t.Fatal(err)
	}
	res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{RequireSigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("without a span claim, A must still fail")
	}
	if v := verdict(t, res, a); v.Reason != domain.ReasonMissing || v.CoveredBy != "" {
		t.Errorf("A = %+v, want an uncovered missing-note failure", v)
	}
}

// The span rides inside the signed payload, so widening it after signing must
// break the signature.
func TestRunRecord_SpanIsCoveredBySignature(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	tip := commit(t, dir, svc, "tip")

	rec := signAs(t, svc, spanRecord(base, tip, "r1"))
	if !rec.VerifySignature() {
		t.Fatal("freshly signed record should verify")
	}
	widened := rec
	widened.CoversFrom = "0000000000000000000000000000000000000000"
	if widened.VerifySignature() {
		t.Error("widening the span after signing must invalidate the signature")
	}
}
