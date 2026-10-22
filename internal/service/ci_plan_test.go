package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestPlanCIProtectedJobDecisions(t *testing.T) {
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
	step := domain.StepRequirements{Command: "true", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	requirements := domain.ExecutionRequirements{Version: 1, Steps: map[domain.StepName]domain.StepRequirements{domain.StepTest: step}}
	other := step
	other.Command = "go test -race ./..."
	policy := domain.CIJobPolicy{Version: 1, Jobs: map[string]*domain.ExecutionRequirements{
		"unit":        &requirements,
		"integration": {Version: 1, Steps: map[domain.StepName]domain.StepRequirements{domain.StepTest: other}},
		"package":     nil,
	}}
	record := domain.RunRecord{CommitSHA: head, StepsRun: []domain.StepName{domain.StepTest}, EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}}, PublicKey: base64.StdEncoding.EncodeToString(pub), Environments: map[domain.StepName]domain.ExecutionEnvironment{domain.StepTest: env}, Contracts: map[domain.StepName]domain.ExecutionContract{domain.StepTest: {Version: 1, CommandDigest: domain.DigestBytes([]byte("true")), ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, TreeSHA: tree, Environment: env}}}
	publish := func() {
		t.Helper()
		payload, err := record.SigningPayload()
		if err != nil {
			t.Fatal(err)
		}
		record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
		if err := svc.Repo().WriteNote(head, record); err != nil {
			t.Fatal(err)
		}
	}
	check := func(key string, reusable bool) {
		t.Helper()
		plan, err := svc.PlanCI(head, policy, key)
		if err != nil {
			t.Fatal(err)
		}
		if plan.SHA != head || len(plan.Jobs) != 3 {
			t.Fatalf("wrong plan: %+v", plan)
		}
		// The mismatching job sorts before the matching one: a failed contract
		// must not poison a later independent decision.
		if plan.Jobs[0].Name != "integration" || plan.Jobs[0].Action != "run" || plan.Jobs[1].Name != "package" || plan.Jobs[1].Action != "run" {
			t.Fatalf("unsafe decisions: %+v", plan)
		}
		if (plan.Jobs[2].Action == "skip") != reusable {
			t.Fatalf("unit reuse=%v plan=%+v", reusable, plan)
		}
		wantRun := 3
		if reusable {
			wantRun = 2
		}
		if len(plan.RunJobs) != wantRun || len(plan.ReusableJobs) != 3-wantRun {
			t.Fatalf("wrong matrix: %+v", plan)
		}
		for _, job := range plan.Jobs {
			if len(job.Reasons) == 0 || job.VerificationMS < 0 {
				t.Fatalf("missing diagnostics: %+v", job)
			}
		}
	}
	// No note exists: every job must run, even with a trusted key.
	check(record.SignerFingerprint(), false)
	publish()
	check(record.SignerFingerprint(), true)
	check("untrusted", false)
	for _, outcome := range []domain.StepOutcome{domain.StepCached, domain.StepSkipped} {
		record.StepOutcomes = map[domain.StepName]domain.StepObservation{domain.StepTest: {Outcome: outcome}}
		publish()
		check(record.SignerFingerprint(), false)
	}
	record.StepOutcomes = nil
	publish()
	// Inspect the actual tampered note, not a mocked verification verdict.
	record.Contracts[domain.StepTest] = domain.ExecutionContract{}
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	check(record.SignerFingerprint(), false)
	// A freshly signed record bound to another commit still cannot skip.
	record.CommitSHA = strings.Repeat("0", 40)
	publish()
	check(record.SignerFingerprint(), false)
	for _, keys := range [][]string{nil, {" "}} {
		if _, err := svc.PlanCI(head, policy, keys...); err == nil {
			t.Fatal("accepted missing protected keys")
		}
	}
	if plan, err := svc.PlanCI("missing-revision", policy, "pinned"); err == nil || len(plan.Jobs) > 0 {
		t.Fatal("returned a partial plan on error")
	}
	if _, err := svc.PlanCI(head, domain.CIJobPolicy{}, "pinned"); err == nil {
		t.Fatal("accepted invalid policy")
	}
}
