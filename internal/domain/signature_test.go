package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// signRecord signs rec with priv the way the runner does: set the public key,
// then sign the SigningPayload (which now includes that key).
func signRecord(t *testing.T, rec *RunRecord, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	rec.PublicKey = base64.StdEncoding.EncodeToString(pub)
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatalf("SigningPayload: %v", err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
}

func TestRunRecord_Signature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := func() RunRecord {
		return RunRecord{
			RunID:             "run_1",
			StepsRun:          []StepName{"lint", "test"},
			Agent:             map[StepName]string{"review": "claude"},
			EvidenceChainRoot: "h0",
			Evidence:          []EvidenceEntry{{Hash: "h0"}, {Hash: "h1", PreviousHash: "h0"}},
		}
	}

	t.Run("unsigned record", func(t *testing.T) {
		rec := base()
		if rec.Signed() || rec.VerifySignature() {
			t.Error("an unsigned record must not report Signed/VerifySignature")
		}
		if rec.SignerFingerprint() != "" {
			t.Error("unsigned record has no fingerprint")
		}
	})

	t.Run("valid signature verifies", func(t *testing.T) {
		rec := base()
		signRecord(t, &rec, pub, priv)
		if !rec.Signed() || !rec.VerifySignature() {
			t.Fatal("a freshly signed record must verify")
		}
		if rec.SignerFingerprint() == "" || rec.SignerFingerprint() != KeyFingerprint(rec.PublicKey) {
			t.Errorf("fingerprint mismatch: %q", rec.SignerFingerprint())
		}
	})

	t.Run("tampered payload breaks the signature", func(t *testing.T) {
		rec := base()
		signRecord(t, &rec, pub, priv)
		rec.StepsRun = append(rec.StepsRun, "smuggled") // change a covered field
		if rec.VerifySignature() {
			t.Error("mutating a signed field must invalidate the signature")
		}
	})

	t.Run("swapping the public key is detected", func(t *testing.T) {
		rec := base()
		signRecord(t, &rec, pub, priv)
		// An attacker swaps in their own key; the signature no longer matches
		// because the key is bound into the signed payload.
		otherPub, _, _ := ed25519.GenerateKey(nil)
		rec.PublicKey = base64.StdEncoding.EncodeToString(otherPub)
		if rec.VerifySignature() {
			t.Error("a swapped public key must fail verification")
		}
	})

	t.Run("garbage fields verify false, not panic", func(t *testing.T) {
		rec := base()
		rec.PublicKey = "not-base64!!"
		rec.Signature = "also-garbage"
		if rec.VerifySignature() {
			t.Error("malformed key/signature must verify false")
		}
	})
}
