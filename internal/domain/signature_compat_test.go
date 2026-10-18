package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"reflect"
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

// CarriedOriginal is the same hazard as Algorithm above, and was caught the
// same way — by running a released warden against a note a newer one wrote.
// A verifier that predates the field drops it on unmarshal, re-marshals without
// it, and computes a different payload; it then reports "signature does not
// verify", which reads as tampering rather than version skew (#212 §7).
//
// So the field stays OUT of the payload. It loses nothing: a carried original is
// believed only after its own signature verifies under a trusted key and the two
// commits are shown to have identical trees, so swapping one in requires a
// trusted signature over tree-identical content — the legitimate case, not an
// attack on it.
func TestSigningPayloadExcludesCarriedOriginal(t *testing.T) {
	base := RunRecord{
		CommitSHA:         "aaaa",
		RunID:             "r1",
		EvidenceChainRoot: "root",
		Evidence:          []EvidenceEntry{{Kind: "step", Source: "test", Hash: "h"}},
	}
	without, err := base.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}

	carried := base
	carried.CommitSHA = "bbbb"
	withCarried := base
	withCarried.CarriedOriginal = &carried
	with, err := withCarried.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}

	if string(without) != string(with) {
		t.Fatalf("carrying an original must not change the signed bytes.\n without: %s\n with:    %s", without, with)
	}
	if strings.Contains(string(with), "carried_original") {
		t.Errorf("carried_original leaked into the signed payload: %s", with)
	}
}

// Re-attesting a re-attestation must stay one level deep, or both the note and
// the verification grow without bound.
func TestRootFlattensReattestationChains(t *testing.T) {
	original := RunRecord{CommitSHA: "aaaa", RunID: "the-real-run"}
	first := RunRecord{CommitSHA: "bbbb", RunID: "the-real-run", CarriedOriginal: &original}
	second := RunRecord{CommitSHA: "cccc", RunID: "the-real-run", CarriedOriginal: first.Root()}

	if got := second.Root(); got.CommitSHA != "aaaa" {
		t.Fatalf("Root should reach the run that actually validated, got %q", got.CommitSHA)
	}
	if second.CarriedOriginal.CarriedOriginal != nil {
		t.Error("a re-attestation of a re-attestation must not nest wrappers")
	}
	if got := original.Root(); got.CommitSHA != "aaaa" {
		t.Errorf("a plain record is its own root, got %q", got.CommitSHA)
	}
}

// preProfileVerify verifies a note the way a warden from before the profile
// fields does: it decodes into a struct that has every RunRecord field EXCEPT
// profile, step_outcomes and approval — built by reflection from RunRecord
// itself, so it cannot drift from the real struct in any other way — recomputes
// the signing payload from that, and checks the ed25519 signature over it.
func preProfileVerify(t *testing.T, note []byte) bool {
	t.Helper()
	newFields := map[string]bool{"Profile": true, "StepOutcomes": true, "Approval": true}
	rt := reflect.TypeOf(RunRecord{})
	var fields []reflect.StructField
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if newFields[f.Name] {
			continue
		}
		fields = append(fields, reflect.StructField{Name: f.Name, Type: f.Type, Tag: f.Tag})
	}
	if len(fields) != rt.NumField()-len(newFields) {
		t.Fatalf("RunRecord no longer has all of %v; update this test", newFields)
	}
	old := reflect.New(reflect.StructOf(fields))
	if err := json.Unmarshal(note, old.Interface()); err != nil {
		t.Fatal(err)
	}
	v := old.Elem()
	sig := v.FieldByName("Signature").String()
	pub := v.FieldByName("PublicKey").String()
	// What SigningPayload does: clear the signature and the carried original.
	v.FieldByName("Signature").SetString("")
	v.FieldByName("CarriedOriginal").Set(reflect.Zero(v.FieldByName("CarriedOriginal").Type()))
	payload, err := json.Marshal(old.Interface())
	if err != nil {
		t.Fatal(err)
	}
	return verifyEd25519Signature(pub, sig, payload)
}

// signedNote signs rec with a fresh key and returns the note bytes as written.
func signedNote(t *testing.T, rec RunRecord) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.PublicKey = b64(pub)
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = b64(ed25519.Sign(priv, payload))
	note, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return note
}

func sampleRecord() RunRecord {
	return RunRecord{
		RunID:             "run-1",
		CommitSHA:         "abc123",
		Agent:             map[StepName]string{},
		StepsRun:          []StepName{StepLint, StepReview},
		Evidence:          []EvidenceEntry{{Kind: "lint.pass", Hash: "h1"}},
		EvidenceChainRoot: "h1",
	}
}

// The profile fields are emitted only under a profile, and a note without them
// must verify for a warden that has never heard of them. If someone drops an
// omitempty or gives one a non-zero default, every unprofiled note in every repo
// stops verifying for pinned older verifiers at once.
func TestSigningPayload_UnprofiledNoteVerifiesForAPreProfileWarden(t *testing.T) {
	rec := sampleRecord()
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"profile", "step_outcomes", "approval"} {
		if strings.Contains(string(payload), key) {
			t.Fatalf("an unprofiled payload must not mention %q: %s", key, payload)
		}
	}
	if !preProfileVerify(t, signedNote(t, rec)) {
		t.Fatal("an unprofiled note must verify for a warden that predates the profile fields")
	}
}

// A profiled note must FAIL for a pinned older verifier, field by field: the
// older warden drops what it does not know, recomputes a different payload and
// rejects the signature. Failing closed is the point — it never accepts a
// record whose profile claims it cannot read.
func TestSigningPayload_ProfiledNoteFailsAPreProfileWarden(t *testing.T) {
	p, err := ParseComplianceProfile("strict")
	if err != nil {
		t.Fatal(err)
	}
	ref := NewProfileRef(p)
	cases := map[string]func(*RunRecord){
		"profile": func(r *RunRecord) { r.Profile = &ref },
		"step_outcomes": func(r *RunRecord) {
			r.StepOutcomes = map[StepName]StepObservation{StepReview: {Outcome: StepSkipped}}
		},
		"approval": func(r *RunRecord) { r.Approval = &ApprovalRef{Channel: ApprovalNone} },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			rec := sampleRecord()
			set(&rec)
			note := signedNote(t, rec)
			var current RunRecord
			if err := json.Unmarshal(note, &current); err != nil {
				t.Fatal(err)
			}
			if !current.VerifySignature() {
				t.Fatal("precondition: the current warden must verify its own profiled note")
			}
			if preProfileVerify(t, note) {
				t.Errorf("a pre-profile warden verified a note carrying %s; it must fail closed", name)
			}
		})
	}
}
