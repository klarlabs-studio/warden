package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

func TestBuildStatement(t *testing.T) {
	rec := &domain.RunRecord{
		RunID:             "run_x",
		Timestamp:         "2026-07-06T00:00:00Z",
		WardenVersion:     "9.9.9",
		CommitSHA:         "abc123",
		StepsRun:          []domain.StepName{"lint", "test"},
		MatchedRules:      []string{"branch=main"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}, {Hash: "h1", PreviousHash: "h0"}},
		Dependencies:      []domain.DependencyManifest{{Ecosystem: "go", Path: "go.sum", Digest: "d1"}},
		PublicKey:         "cHVia2V5", // not a real key; SignerFingerprint just hashes it
		Signature:         "c2ln",     // presence is what makes Signed() true here
	}
	res := service.VerifyResult{SHA: "abc123", Record: rec, Validated: true, Signed: true, SignatureValid: true, Trusted: true}

	st := buildStatement(res)

	if st.Type != statementType || st.PredicateType != wardenPredicateID {
		t.Errorf("wrong envelope: type=%q predicateType=%q", st.Type, st.PredicateType)
	}
	if len(st.Subject) != 1 || st.Subject[0].Digest["gitCommit"] != "abc123" {
		t.Errorf("subject must digest the commit: %+v", st.Subject)
	}
	p := st.Predicate
	if p.RunID != "run_x" || p.WardenVersion != "9.9.9" || len(p.StepsRun) != 2 {
		t.Errorf("predicate not projected faithfully: %+v", p)
	}
	if p.Evidence == nil || p.Evidence.ChainRoot != "h0" || len(p.Evidence.Entries) != 2 {
		t.Errorf("evidence block missing/wrong: %+v", p.Evidence)
	}
	if len(p.Dependencies) != 1 || p.Dependencies[0].Ecosystem != "go" {
		t.Errorf("dependencies (SBOM) not carried: %+v", p.Dependencies)
	}
	if p.Signer == nil || p.Signer.PublicKey != "cHVia2V5" {
		t.Errorf("signer block missing: %+v", p.Signer)
	}
	if !p.Verification.Attested || !p.Verification.SignatureValid || !p.Verification.Trusted {
		t.Errorf("verification block wrong: %+v", p.Verification)
	}

	// An unsigned, untrusted note still attests (chain intact), but reports so.
	res2 := service.VerifyResult{SHA: "abc123", Record: rec, Validated: false, Signed: false}
	// clear the signature-bearing field so Signed() is false
	recUnsigned := *rec
	recUnsigned.PublicKey = ""
	recUnsigned.Signature = ""
	res2.Record = &recUnsigned
	st2 := buildStatement(res2)
	if st2.Predicate.Verification.Trusted {
		t.Error("untrusted note must not report trusted")
	}
	if st2.Predicate.Signer != nil {
		t.Error("an unsigned note must not carry a signer block")
	}
}

// TestCmdAttest_EndToEnd drives the command: a noted commit yields a valid
// in-toto statement; an un-noted commit is nothing to attest (exit 1).
func TestCmdAttest_EndToEnd(t *testing.T) {
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	// No note yet → nothing to attest.
	if code := cmdAttest([]string{"--commit", head}, &out, &errb); code != 1 {
		t.Fatalf("un-noted commit: expected exit 1, got %d (err=%q)", code, errb.String())
	}
	if !strings.Contains(errb.String(), "nothing to attest") {
		t.Errorf("expected a 'nothing to attest' message, got %q", errb.String())
	}

	// Attach an attesting note, then attest.
	rec := domain.RunRecord{
		RunID:             "run_e2e",
		CommitSHA:         head,
		StepsRun:          []domain.StepName{"lint"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
	}
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := cmdAttest([]string{"--commit", head}, &out, &errb); code != 0 {
		t.Fatalf("noted commit: expected exit 0, got %d (err=%q)", code, errb.String())
	}
	var st intotoStatement
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("attest did not emit valid JSON: %v\n%s", err, out.String())
	}
	if st.PredicateType != wardenPredicateID || st.Subject[0].Digest["gitCommit"] != head {
		t.Errorf("statement wrong: %+v", st)
	}
	if st.Predicate.RunID != "run_e2e" || !st.Predicate.Verification.Attested {
		t.Errorf("predicate wrong: %+v", st.Predicate)
	}
}
