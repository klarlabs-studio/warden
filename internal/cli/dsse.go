package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// DSSE is the envelope format for a signed in-toto statement
// (https://github.com/secure-systems-lab/dsse).
//
// Warden emits one so its claim can be verified by whoever ends up holding it,
// using warden's public key and nothing else. A bare statement has to be
// re-signed by whatever carries it downstream — a build platform, a registry
// tool — and at that point the signature attests to the carrier rather than to
// warden. A consumer reading it can then only conclude "the carrier says warden
// said this", which is a materially weaker claim than warden saying it.
type DSSE struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []DSSESignature `json:"signatures"`
}

// DSSESignature is one signature over the envelope's payload.
type DSSESignature struct {
	// KeyID is warden's signer fingerprint, so a verifier can select the right
	// public key from a roster without trial verification.
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// intotoPayloadType is the media type DSSE requires for in-toto statements. It
// is part of what gets signed, so it cannot be varied.
const intotoPayloadType = "application/vnd.in-toto+json"

// payloadSigner signs the pre-authentication encoding and names the key it
// used. The service implements it; a test can substitute one.
type payloadSigner interface {
	SignPayload(payload []byte) (signature, fingerprint string, err error)
}

// signStatement wraps a statement in a signed DSSE envelope.
func signStatement(statement any, signer payloadSigner) (DSSE, error) {
	payload, err := json.Marshal(statement)
	if err != nil {
		return DSSE{}, fmt.Errorf("encode statement: %w", err)
	}

	sig, keyID, err := signer.SignPayload(pae(intotoPayloadType, payload))
	if err != nil {
		return DSSE{}, fmt.Errorf("sign statement: %w", err)
	}

	return DSSE{
		PayloadType: intotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []DSSESignature{{KeyID: keyID, Sig: sig}},
	}, nil
}

// pae is DSSE's Pre-Authentication Encoding.
//
// Signing the payload alone would let an attacker re-present the same bytes
// under a different payload type and have the signature still verify. PAE binds
// the type and the length of each field into what is signed, so the framing is
// covered too:
//
//	"DSSEv1" SP len(type) SP type SP len(body) SP body
//
// The exact byte layout is the interoperability contract — any DSSE verifier
// reconstructs this to check the signature, so it must match to the space.
func pae(payloadType string, payload []byte) []byte {
	return fmt.Appendf(nil, "DSSEv1 %d %s %d %s",
		len(payloadType), payloadType, len(payload), payload)
}
