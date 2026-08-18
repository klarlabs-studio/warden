package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// fixedSigner signs with a known key so a test can verify the envelope the way
// an outside consumer would.
type fixedSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	id   string
}

func newFixedSigner(t *testing.T) *fixedSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fixedSigner{priv: priv, pub: pub, id: "cafebabe12345678"}
}

func (f *fixedSigner) SignPayload(payload []byte) (string, string, error) {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, payload)), f.id, nil
}

type failingSigner struct{}

func (failingSigner) SignPayload([]byte) (string, string, error) {
	return "", "", errNoKey
}

var errNoKey = &noKeyError{}

type noKeyError struct{}

func (*noKeyError) Error() string { return "no signing key available" }

func TestTheEnvelopeVerifiesWithNothingButThePublicKey(t *testing.T) {
	signer := newFixedSigner(t)
	statement := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://slsa.dev/verification_summary/v1",
	}

	env, err := signStatement(statement, signer)
	if err != nil {
		t.Fatalf("signStatement: %v", err)
	}

	// Reconstruct the way any DSSE verifier does — no warden code involved.
	// This is the whole point: the claim must stand on warden's key alone,
	// without the reader trusting whatever carried it.
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(signer.pub, pae(env.PayloadType, payload), sig) {
		t.Fatal("an outside verifier could not check warden's signature")
	}
}

func TestATamperedPayloadFailsVerification(t *testing.T) {
	signer := newFixedSigner(t)
	env, err := signStatement(map[string]any{"verificationResult": "PASSED"}, signer)
	if err != nil {
		t.Fatal(err)
	}

	tampered, err := json.Marshal(map[string]any{"verificationResult": "FAILED-BUT-CLAIMED-PASSED"})
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)

	if ed25519.Verify(signer.pub, pae(env.PayloadType, tampered), sig) {
		t.Error("a rewritten verdict still verified")
	}
}

// TestThePayloadTypeIsCoveredBySignature is why PAE exists rather than signing
// the payload alone: without it an attacker could re-present identical bytes
// under a different media type and the signature would still check out.
func TestThePayloadTypeIsCoveredBySignature(t *testing.T) {
	signer := newFixedSigner(t)
	env, err := signStatement(map[string]any{"a": 1}, signer)
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	sig, _ := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)

	if ed25519.Verify(signer.pub, pae("application/vnd.something-else", payload), sig) {
		t.Error("the payload type is not covered by the signature")
	}
}

// TestPAEMatchesTheSpecByte pins the exact encoding. It is the
// interoperability contract: every DSSE verifier reconstructs this string, so
// a stray space would make warden's envelopes unverifiable everywhere while
// still round-tripping through warden itself.
func TestPAEMatchesTheSpecByte(t *testing.T) {
	got := string(pae("application/vnd.in-toto+json", []byte(`{"a":1}`)))
	want := `DSSEv1 28 application/vnd.in-toto+json 7 {"a":1}`

	if got != want {
		t.Errorf("pae =\n  %q\nwant\n  %q", got, want)
	}
}

func TestTheEnvelopeNamesTheKeyThatSignedIt(t *testing.T) {
	signer := newFixedSigner(t)
	env, err := signStatement(map[string]any{"a": 1}, signer)
	if err != nil {
		t.Fatal(err)
	}

	// A verifier holding a roster selects by keyid rather than trying each key
	// in turn.
	if env.Signatures[0].KeyID != signer.id {
		t.Errorf("keyid = %q, want the signer's fingerprint", env.Signatures[0].KeyID)
	}
	if env.PayloadType != "application/vnd.in-toto+json" {
		t.Errorf("payloadType = %q", env.PayloadType)
	}
}

func TestSigningWithoutAKeyIsAnErrorNotAnUnsignedEnvelope(t *testing.T) {
	_, err := signStatement(map[string]any{"a": 1}, failingSigner{})

	// A caller who asked for a signature and silently got none would ship an
	// envelope nobody can verify.
	if err == nil {
		t.Fatal("signStatement returned an envelope with no signature")
	}
	if !strings.Contains(err.Error(), "no signing key") {
		t.Errorf("err = %v", err)
	}
}
