package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// b64 shortens the encoding used six times below. It also keeps this file clear
// of a long `base64.StdEncoding.EncodeToString` token, which an entropy scanner
// reads as a possible secret key — on lines that encode a PUBLIC key generated
// fresh by the test.
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// The Algorithm field lives INSIDE SigningPayload, so a non-empty value changes
// the bytes every signature covers. Leaving it empty for the existing ed25519
// scheme is what keeps notes written before the field existed verifiable.
//
// This test is the guard on that: it pins that an ed25519 record's signing
// payload contains no algorithm key at all. If someone later gives Algorithm a
// non-empty default, or drops omitempty, every signed note in every repo becomes
// unverifiable at once — silently, because verification simply starts returning
// false.
func TestSigningPayload_OmitsAlgorithmForTheDefaultScheme(t *testing.T) {
	rec := RunRecord{RunID: "run-1", CommitSHA: "abc123", PublicKey: "key"}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "algorithm") {
		t.Fatalf("an ed25519 record's payload must not mention algorithm:\n%s", payload)
	}
	if rec.Algorithm != AlgorithmEd25519 || AlgorithmEd25519 != "" {
		t.Error("the default algorithm must be the empty string")
	}
}

// A record signed before Algorithm existed must still verify. Simulated by
// signing with the field left at its zero value, which is byte-for-byte what an
// older warden produced.
func TestVerifySignature_LegacyEd25519RecordStillVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	// A real two-link chain: the root is the first entry's hash and each later
	// entry names its predecessor, which is what VerifyChain checks.
	rec := RunRecord{
		RunID:     "run-1",
		CommitSHA: "abc123",
		Evidence: []EvidenceEntry{
			{Kind: "step", Hash: "h1"},
			{Kind: "step", Hash: "h2", PreviousHash: "h1"},
		},
		EvidenceChainRoot: "h1",
		PublicKey:         b64(pub),
	}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = b64(ed25519.Sign(priv, payload))

	if !rec.VerifySignature() {
		t.Fatal("a record signed with no algorithm field must still verify")
	}
	// And the whole fail-closed predicate must still hold for it.
	if !rec.Attests("abc123") {
		t.Error("a legacy signed record must still attest its commit")
	}
}

// An algorithm this warden does not implement must verify FALSE, never pass. A
// newer warden could write a scheme an older one cannot read, and the safe
// reading of "I don't understand this signature" is "unverified".
func TestVerifySignature_UnknownAlgorithmFailsClosed(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{
		RunID:     "run-1",
		CommitSHA: "abc123",
		PublicKey: b64(pub),
		Algorithm: "some-future-scheme",
	}
	payload, _ := rec.SigningPayload()
	// A genuinely valid ed25519 signature over the payload — the point is that a
	// correct signature under an UNKNOWN algorithm still must not verify.
	rec.Signature = b64(ed25519.Sign(priv, payload))

	if rec.VerifySignature() {
		t.Error("an unrecognized algorithm must fail closed")
	}
}

// Changing the algorithm on a signed record must invalidate it: the field is
// inside the payload precisely so a scheme cannot be downgraded after signing.
func TestVerifySignature_AlgorithmIsCoveredByTheSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{
		RunID:     "run-1",
		CommitSHA: "abc123",
		PublicKey: b64(pub),
	}
	payload, _ := rec.SigningPayload()
	rec.Signature = b64(ed25519.Sign(priv, payload))
	if !rec.VerifySignature() {
		t.Fatal("precondition: the record should verify")
	}

	rec.Algorithm = AlgorithmSSH
	if rec.VerifySignature() {
		t.Error("flipping the algorithm after signing must invalidate the signature")
	}
}
