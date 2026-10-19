package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestVerifyRequirementsTrustedContract(t *testing.T) {
	svc, err := New(initRepo(t), "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := svc.repo.TreeSHA(head)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	digest := domain.DigestBytes([]byte("fixture"))
	env := domain.ExecutionEnvironment{Runtime: "docker", ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	req := domain.StepRequirements{Command: "true", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	requirements := domain.ExecutionRequirements{Version: 1, Steps: map[domain.StepName]domain.StepRequirements{domain.StepTest: req}}
	contract := domain.ExecutionContract{Version: 1, CommandDigest: domain.DigestBytes([]byte("true")), ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, TreeSHA: tree, Environment: env}
	record := domain.RunRecord{CommitSHA: head, StepsRun: []domain.StepName{domain.StepTest}, EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}}, PublicKey: base64.StdEncoding.EncodeToString(pub), Environments: map[domain.StepName]domain.ExecutionEnvironment{domain.StepTest: env}, Contracts: map[domain.StepName]domain.ExecutionContract{domain.StepTest: contract}}
	publish := func() {
		t.Helper()
		payload, e := record.SigningPayload()
		if e != nil {
			t.Fatal(e)
		}
		record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
		if e = svc.Repo().WriteNote(head, record); e != nil {
			t.Fatal(e)
		}
	}
	verify := func(want bool) {
		t.Helper()
		res, e := svc.VerifyRequirements(head, ExternalReject, requirements, record.SignerFingerprint())
		if e != nil {
			t.Fatal(e)
		}
		if res.Validated != want {
			t.Fatalf("validated=%v failures=%v", res.Validated, res.RequirementFailures)
		}
	}
	publish()
	verify(true)
	req.Command = "false"
	requirements.Steps[domain.StepTest] = req
	verify(false)
	req.Command = "true"
	requirements.Steps[domain.StepTest] = req
	contract.TreeSHA = strings.Repeat("0", 40)
	record.Contracts[domain.StepTest] = contract
	publish()
	verify(false)
	contract.TreeSHA = tree
	record.Contracts[domain.StepTest] = contract
	publish()
	verify(true)
	delete(record.Environments, domain.StepTest)
	publish()
	verify(false)
	record.Environments[domain.StepTest] = env
	record.Contracts = nil
	publish()
	verify(false)
	record.Contracts = map[domain.StepName]domain.ExecutionContract{domain.StepTest: contract}
	publish()
	contract.ContextDigest = "tampered"
	record.Contracts[domain.StepTest] = contract
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	verify(false)
}

// A version 2 requirements file's profile is enforced alongside --min-profile,
// with no steps required.
func TestVerifyRequirementsV2EnforcesProfile(t *testing.T) {
	svc, err := New(initRepo(t), "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	rec := strictConformingRecord(t, head)
	rec.PublicKey = base64.StdEncoding.EncodeToString(pub)
	ref := domain.NewProfileRef(mustProfile(t, "standard"))
	rec.Profile = &ref
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}
	fp := rec.SignerFingerprint()
	for _, tt := range []struct {
		fileMin string
		flag    []domain.ComplianceProfile
		want    bool
	}{
		{"standard", nil, true},
		{"strict", nil, false},
		{"standard", []domain.ComplianceProfile{mustProfile(t, "strict")}, false},
		{"permissive", []domain.ComplianceProfile{mustProfile(t, "standard")}, true},
	} {
		req := domain.ExecutionRequirements{Version: 2, Profile: &domain.ProfileRequirement{Min: tt.fileMin}}
		res, err := svc.VerifyRequirementsWithProfiles(head, ExternalReject, req, tt.flag, fp)
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated != tt.want {
			t.Errorf("file %s + flag %v: validated=%v want %v (%v)", tt.fileMin, tt.flag, res.Validated, tt.want, res.RequirementFailures)
		}
		if len(res.Conformance) != 1+len(tt.flag) {
			t.Errorf("want a report per minimum, got %d", len(res.Conformance))
		}
	}
}
