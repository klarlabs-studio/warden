package domain

import (
	"errors"
	"strings"
	"testing"
)

func validExternal() ExternalRunRef {
	return ExternalRunRef{
		Provider:   "github-actions",
		RunID:      "30747937107",
		Repository: "klarlabs-studio/warden",
		Commit:     "57ad2eae4fa0a1d117d877c478890295a3a90884",
		Checks:     []string{"lint", "test"},
	}
}

func signedExternalRecord() RunRecord {
	ext := validExternal()
	return RunRecord{
		CommitSHA:   ext.Commit,
		ExternalRun: &ext,
		Signature:   "not-checked-here",
		PublicKey:   "not-checked-here",
	}
}

// A run against a different tree proves nothing about this commit. Accepting a
// mismatch is exactly how "CI passed" degrades into "some CI passed, somewhere".
func TestExternalRun_MustDescribeTheCommitItAttests(t *testing.T) {
	rec := signedExternalRecord()
	rec.ExternalRun.Commit = "0000000000000000000000000000000000000000"

	err := rec.ValidateExternal()
	if !errors.Is(err, ErrExternalRunInvalid) {
		t.Fatalf("err = %v, want ErrExternalRunInvalid", err)
	}
	if !strings.Contains(err.Error(), "attests") {
		t.Errorf("the message must say what it attests versus what ran: %v", err)
	}
}

// A prefix is not a match: a run against any commit sharing a short prefix would
// otherwise stand in for this one.
func TestExternalRun_BoundToRejectsAPrefix(t *testing.T) {
	e := validExternal()
	if e.BoundTo(e.Commit[:12]) {
		t.Error("a 12-char prefix must not satisfy the binding")
	}
	if !e.BoundTo(e.Commit) {
		t.Error("the full sha must satisfy it")
	}
}

// An unsigned external note reaches a consumer with no pinned signer as a bare
// "checks passed", indistinguishable from a local attestation. That is the one
// downgrade the SigningPayload mechanism does not already close, so it is closed
// here instead.
func TestExternalRun_MustBeSigned(t *testing.T) {
	rec := signedExternalRecord()
	rec.Signature = ""

	err := rec.ValidateExternal()
	if !errors.Is(err, ErrExternalRunInvalid) {
		t.Fatalf("err = %v, want ErrExternalRunInvalid", err)
	}
	if !strings.Contains(err.Error(), "signed") {
		t.Errorf("the message must name signing as the reason: %v", err)
	}
}

// Each field Validate requires is one without which the claim cannot be checked
// by anybody.
func TestExternalRun_RejectsAReferenceNobodyCouldCheck(t *testing.T) {
	cases := map[string]func(*ExternalRunRef){
		"no provider":   func(e *ExternalRunRef) { e.Provider = "" },
		"no run id":     func(e *ExternalRunRef) { e.RunID = "" },
		"no repository": func(e *ExternalRunRef) { e.Repository = "" },
		"no commit":     func(e *ExternalRunRef) { e.Commit = "" },
		// A reference naming no checks asserts that a run happened, not that
		// anything passed — an attestation vouching for nothing.
		"no checks": func(e *ExternalRunRef) { e.Checks = nil },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			e := validExternal()
			break_(&e)
			if err := e.Validate(); !errors.Is(err, ErrExternalRunInvalid) {
				t.Errorf("err = %v, want ErrExternalRunInvalid", err)
			}
		})
	}
}

func TestExternalRun_AcceptsAUsableReference(t *testing.T) {
	if err := validExternal().Validate(); err != nil {
		t.Fatalf("a complete reference must validate: %v", err)
	}
	if err := signedExternalRecord().ValidateExternal(); err != nil {
		t.Fatalf("a signed, bound record must validate: %v", err)
	}
}

// A record with no external reference is a local attestation and must be
// unaffected — ValidateExternal is a no-op for it.
func TestExternalRun_LocalRecordsAreUntouched(t *testing.T) {
	var rec RunRecord
	if rec.IsExternal() {
		t.Error("a record with no reference must not read as external")
	}
	if err := rec.ValidateExternal(); err != nil {
		t.Errorf("a local record must not be subject to external rules: %v", err)
	}
}

// The field must be invisible when unused, or every note written before it
// existed stops verifying — the same requirement Algorithm and AgentTrace have.
func TestExternalRun_OmittedWhenAbsent(t *testing.T) {
	rec := RunRecord{RunID: "run-1", CommitSHA: "abc"}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "external_run") {
		t.Errorf("an unused external_run must not appear in the payload:\n%s", payload)
	}
}

// And when present it must be COVERED by the signature: a reference bound
// alongside the record rather than inside it could be attached to, or stripped
// from, a signed note.
func TestExternalRun_IsInsideTheSignedPayload(t *testing.T) {
	rec := signedExternalRecord()
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), rec.ExternalRun.RunID) {
		t.Error("the run id must be inside the bytes the signature covers")
	}
}
