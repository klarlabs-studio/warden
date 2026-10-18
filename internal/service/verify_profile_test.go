package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func mustProfile(t *testing.T, s string) domain.ComplianceProfile {
	t.Helper()
	p, err := domain.ParseComplianceProfile(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// strictConformingRecord is a record that meets every clause strict@1 can be
// checked against in this release: each required step executed, each required
// shell step in a recorded Linux container with network none, no approval
// needed.
func strictConformingRecord(t *testing.T, head string) domain.RunRecord {
	t.Helper()
	ref := domain.NewProfileRef(mustProfile(t, "strict"))
	digest := domain.DigestBytes([]byte("image"))
	env := domain.ExecutionEnvironment{Runtime: "docker", Image: "checks:1", ImageID: digest, ImageIDKind: "config", Platform: "linux/amd64", Network: "none"}
	return domain.RunRecord{
		CommitSHA:         head,
		StepsRun:          []domain.StepName{domain.StepCredentials, domain.StepLint, domain.StepTest, domain.StepSecurityScan, domain.StepReview},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
		Environments: map[domain.StepName]domain.ExecutionEnvironment{
			domain.StepLint: env, domain.StepTest: env, domain.StepSecurityScan: env,
		},
		Profile: &ref,
		StepOutcomes: map[domain.StepName]domain.StepObservation{
			domain.StepCredentials:  {Outcome: domain.StepExecuted},
			domain.StepLint:         {Outcome: domain.StepExecuted},
			domain.StepTest:         {Outcome: domain.StepExecuted},
			domain.StepSecurityScan: {Outcome: domain.StepExecuted},
			domain.StepReview:       {Outcome: domain.StepExecuted, Agent: "claude"},
		},
		Approval: &domain.ApprovalRef{Channel: domain.ApprovalNone},
	}
}

// Every case writes a really signed note and verifies it through
// VerifyMinProfile with the signer pinned, so the observations under test are
// the ones the signature covers.
func TestConformanceAgainstSignedRecords(t *testing.T) {
	svc, err := New(initRepo(t), "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	tests := []struct {
		name   string
		min    string
		mutate func(*domain.RunRecord)
		// wantFail is a substring of a failure; "" means the note conforms.
		wantFail string
		// keys overrides the pinned keys (default: the signer).
		keys []string
	}{
		{name: "strict note meets strict", min: "strict"},
		{name: "strict note meets standard", min: "standard"},
		{name: "strict note meets permissive", min: "permissive"},
		{name: "explicit version", min: "strict@1"},
		{
			name: "network bridge fails strict (F8)", min: "strict",
			mutate: func(r *domain.RunRecord) {
				env := r.Environments[domain.StepTest]
				env.Network = "bridge"
				r.Environments[domain.StepTest] = env
			},
			wantFail: `test ran with network "bridge"`,
		},
		{
			name: "network bridge is not a standard clause", min: "standard",
			mutate: func(r *domain.RunRecord) {
				env := r.Environments[domain.StepTest]
				env.Network = "bridge"
				r.Environments[domain.StepTest] = env
			},
		},
		{
			name: "skipped required review fails strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepReview] = domain.StepObservation{Outcome: domain.StepSkipped}
			},
			wantFail: "review was skipped",
		},
		{
			name: "skipped required test fails standard", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepTest] = domain.StepObservation{Outcome: domain.StepSkipped}
			},
			wantFail: "test was skipped",
		},
		{
			name: "cached required step fails strict (Q14)", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepLint] = domain.StepObservation{Outcome: domain.StepCached}
			},
			wantFail: "lint was cached",
		},
		{
			name: "predicted required step fails strict (Q14)", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepLint] = domain.StepObservation{Outcome: domain.StepPredicted}
			},
			wantFail: "lint was predicted",
		},
		{
			name: "cached required step counts for standard", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepLint] = domain.StepObservation{Outcome: domain.StepCached}
			},
		},
		{
			name: "required step with no outcome fails", min: "standard",
			mutate: func(r *domain.RunRecord) {
				delete(r.StepOutcomes, domain.StepCredentials)
			},
			wantFail: "credentials has no recorded outcome",
		},
		{
			name: "host security-scan fails strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				delete(r.Environments, domain.StepSecurityScan)
			},
			wantFail: "security-scan has no complete container execution record",
		},
		{
			name: "auto approval where required fails standard", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.Approval = &domain.ApprovalRef{Required: true, Channel: domain.ApprovalAuto, Principal: "warden-auto"}
			},
			wantFail: "answered automatically",
		},
		{
			name: "tty approval where required passes strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.Approval = &domain.ApprovalRef{Required: true, Channel: domain.ApprovalTTY, Principal: "warden-cli"}
			},
		},
		{
			name: "auto approval is not a permissive clause", min: "permissive",
			mutate: func(r *domain.RunRecord) {
				r.Approval = &domain.ApprovalRef{Required: true, Channel: domain.ApprovalAuto}
			},
		},
		{
			name: "missing approval record fails standard", min: "standard",
			mutate:   func(r *domain.RunRecord) { r.Approval = nil },
			wantFail: "records no approval gate",
		},
		{
			name: "channel without a required decision fails", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.Approval = &domain.ApprovalRef{Channel: domain.ApprovalAuto}
			},
			wantFail: "no decision was required",
		},
		{
			name: "digest mismatch fails closed", min: "permissive",
			mutate: func(r *domain.RunRecord) {
				r.Profile.Digest = domain.DigestBytes([]byte("a fork's strict"))
			},
			wantFail: "digest differs",
		},
		{
			name: "unknown version fails closed", min: "permissive",
			mutate: func(r *domain.RunRecord) {
				r.Profile = &domain.ProfileRef{Name: "strict", Version: 2, Digest: r.Profile.Digest}
			},
			wantFail: "strict@2 is not a profile this warden knows",
		},
		{
			name: "below the minimum rank fails", min: "strict",
			mutate: func(r *domain.RunRecord) {
				ref := domain.NewProfileRef(mustProfile(t, "standard"))
				r.Profile = &ref
			},
			wantFail: "gated under standard@1",
		},
		{
			name: "unprofiled note fails", min: "permissive",
			mutate:   func(r *domain.RunRecord) { r.Profile = nil },
			wantFail: "records no profile",
		},
		{
			name: "untrusted signer fails before any observation is read", min: "permissive",
			keys:     []string{strings.Repeat("ab", 8)},
			wantFail: "trusted, commit-bound local provenance",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := strictConformingRecord(t, head)
			rec.PublicKey = pubB64
			if tt.mutate != nil {
				tt.mutate(&rec)
			}
			payload, err := rec.SigningPayload()
			if err != nil {
				t.Fatal(err)
			}
			rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
			if err := svc.Repo().WriteNote(head, rec); err != nil {
				t.Fatal(err)
			}
			keys := tt.keys
			if keys == nil {
				keys = []string{rec.SignerFingerprint()}
			}
			min := mustProfile(t, tt.min)
			res, err := svc.VerifyMinProfile(head, ExternalReject, []domain.ComplianceProfile{min}, keys...)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Conformance) != 1 {
				t.Fatalf("want one conformance report, got %+v", res.Conformance)
			}
			rep := res.Conformance[0]
			if tt.wantFail == "" {
				if !res.Validated || !rep.OK() || len(res.RequirementFailures) != 0 {
					t.Fatalf("want conformance, got validated=%v failures=%v", res.Validated, res.RequirementFailures)
				}
				return
			}
			if res.Validated || rep.OK() {
				t.Fatalf("want a failure containing %q, but the note was accepted", tt.wantFail)
			}
			if !slices.ContainsFunc(res.RequirementFailures, func(f string) bool { return strings.Contains(f, tt.wantFail) }) {
				t.Fatalf("failures %q do not contain %q", res.RequirementFailures, tt.wantFail)
			}
			for _, f := range res.RequirementFailures {
				if !strings.HasPrefix(f, "profile "+min.String()+": ") {
					t.Errorf("failure %q does not name the profile", f)
				}
			}
		})
	}
}

// F8: the gap Conformance closes. ContainerChecks — what --container-steps
// uses — accepts a container record with network bridge; strict must not.
func TestConformanceChecksNetworkThatContainerChecksIgnores(t *testing.T) {
	rec := strictConformingRecord(t, "abc")
	env := rec.Environments[domain.StepLint]
	env.Network = "bridge"
	rec.Environments[domain.StepLint] = env
	if !rec.ContainerChecks([]domain.StepName{domain.StepLint, domain.StepTest, domain.StepSecurityScan}, "") {
		t.Fatal("test premise: ContainerChecks should accept a bridge-network record")
	}
	rep := Conformance(mustProfile(t, "strict"), VerifyResult{Validated: true, Trusted: true, Record: &rec})
	if rep.OK() || !strings.Contains(strings.Join(rep.Failures, "\n"), `lint ran with network "bridge"`) {
		t.Fatalf("strict accepted a bridge-network check: %+v", rep)
	}
}

// A report never lists a clause as checked that it did not evaluate: strict's
// isolation and authorship clauses have no signed observations yet, so they
// are reported as deferred and are absent from Checked.
func TestConformanceReportsDeferredClausesSeparately(t *testing.T) {
	rec := strictConformingRecord(t, "abc")
	res := VerifyResult{Validated: true, Trusted: true, Record: &rec}

	strict := Conformance(mustProfile(t, "strict"), res)
	if !strict.OK() {
		t.Fatalf("baseline must conform: %v", strict.Failures)
	}
	if len(strict.Deferred) != 2 {
		t.Fatalf("strict must defer isolation and authorship, got %q", strict.Deferred)
	}
	for _, d := range strict.Deferred {
		if slices.Contains(strict.Checked, d) {
			t.Errorf("deferred clause %q is also listed as checked", d)
		}
	}
	joined := strings.Join(strict.Checked, "\n")
	for _, want := range []string{"network none", "Linux container", "executed", "not auto-approved", "built-in definition", "ranks at least strict@1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("strict checked clauses lack %q:\n%s", want, joined)
		}
	}

	standard := Conformance(mustProfile(t, "standard"), res)
	if len(standard.Deferred) != 0 || strings.Contains(strings.Join(standard.Checked, "\n"), "network") {
		t.Errorf("standard must check no container clause and defer nothing: %+v", standard)
	}

	// An untrusted result evaluates the trust clause alone.
	untrusted := Conformance(mustProfile(t, "strict"), VerifyResult{Validated: true, Record: &rec})
	if untrusted.OK() || len(untrusted.Checked) != 1 {
		t.Fatalf("untrusted result must stop at the trust clause: %+v", untrusted)
	}
	external := Conformance(mustProfile(t, "permissive"), VerifyResult{Validated: true, Trusted: true, External: true, Record: &rec})
	if external.OK() {
		t.Fatal("an external attestation must not conform")
	}
}
