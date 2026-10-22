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
// shell step in a recorded Linux container with network none, the review agent
// isolated with an unchanged tree and the allowlisted environment, no approval
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
			domain.StepReview:       {Outcome: domain.StepExecuted, Agent: "claude", Isolated: yes(), TreeUnchanged: yes(), EnvAllowlisted: yes()},
		},
		Approval: &domain.ApprovalRef{Channel: domain.ApprovalNone},
	}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// editReview rewrites the review's observation in place.
func editReview(r *domain.RunRecord, edit func(*domain.StepObservation)) {
	o := r.StepOutcomes[domain.StepReview]
	edit(&o)
	r.StepOutcomes[domain.StepReview] = o
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
			name: "declared agent without a trace fails strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.Authorship = &domain.AuthorshipSignals{Trailers: []domain.TrailerDeclaration{{Commit: head, Agent: "claude-code", Trailer: "Claude-Session: u"}}}
			},
			wantFail: "no Agent Trace was notarized",
		},
		{
			name: "environment declaration without a trace fails strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.Authorship = &domain.AuthorshipSignals{EnvMarkers: []domain.EnvMarkerDeclaration{{Name: "GEMINI_CLI", Agent: "gemini-cli"}}}
			},
			wantFail: "gemini-cli",
		},
		{
			name: "trace reference without a digest is no trace", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.Authorship = &domain.AuthorshipSignals{EnvMarkers: []domain.EnvMarkerDeclaration{{Name: "GEMINI_CLI", Agent: "gemini-cli"}}}
				r.AgentTrace = &domain.AgentTraceRef{Path: ".agent-trace/run.json"}
			},
			wantFail: "no Agent Trace was notarized",
		},
		{
			name: "declared agent with a notarized trace meets strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				r.Authorship = &domain.AuthorshipSignals{Trailers: []domain.TrailerDeclaration{{Commit: head, Agent: "claude-code", Trailer: "Claude-Session: u"}}}
				r.AgentTrace = &domain.AgentTraceRef{Digest: strings.Repeat("ab", 32), Path: ".agent-trace/run.json"}
			},
		},
		{
			name: "declared agent without a trace is not a standard clause", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.Authorship = &domain.AuthorshipSignals{Trailers: []domain.TrailerDeclaration{{Commit: head, Agent: "claude-code", Trailer: "Claude-Session: u"}}}
			},
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
			name: "agent step without an isolation observation fails standard", min: "standard",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.Isolated, o.TreeUnchanged = nil, nil })
			},
			wantFail: "review has no isolation observation",
		},
		{
			name: "isolated agent step without a tree guard observation fails", min: "strict",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.TreeUnchanged = nil })
			},
			wantFail: "review has no tree guard observation",
		},
		{
			name: "agent step that changed the tree fails", min: "standard",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.TreeUnchanged = no() })
			},
			wantFail: "review changed the tree",
		},
		{
			name: "agent step without the allowlisted environment fails strict", min: "strict",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.EnvAllowlisted = nil })
			},
			wantFail: "review has no allowlisted-environment observation",
		},
		{
			name: "the allowlisted environment is not a standard clause", min: "standard",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.EnvAllowlisted = nil })
			},
		},
		{
			name: "isolation is not a permissive clause", min: "permissive",
			mutate: func(r *domain.RunRecord) {
				editReview(r, func(o *domain.StepObservation) { o.Isolated, o.TreeUnchanged, o.EnvAllowlisted = nil, nil, nil })
			},
		},
		{
			name: "a cached agent step ran no agent, so needs no boundary", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.StepOutcomes[domain.StepDocument] = domain.StepObservation{Outcome: domain.StepCached}
			},
		},
		{
			name: "a step the policy handed an agent needs a boundary even unnamed", min: "standard",
			mutate: func(r *domain.RunRecord) {
				r.Agent = map[domain.StepName]string{"docs": "claude"}
				r.StepOutcomes["docs"] = domain.StepObservation{Outcome: domain.StepExecuted}
			},
			wantFail: "docs has no isolation observation",
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
				r.Profile = &domain.ProfileRef{Name: "strict", Version: 3, Digest: r.Profile.Digest}
			},
			wantFail: "strict@3 is not a profile this warden knows",
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

// Every clause strict@1 defines is now read from the signed record, so strict
// defers nothing. Deferred stays in the report so that a clause a later
// definition adds before the record carries its observation can never be
// reported as checked.
func TestConformanceChecksEveryStrictClause(t *testing.T) {
	rec := strictConformingRecord(t, "abc")
	res := VerifyResult{Validated: true, Trusted: true, Record: &rec}

	strict := Conformance(mustProfile(t, "strict"), res)
	if !strict.OK() {
		t.Fatalf("baseline must conform: %v", strict.Failures)
	}
	if len(strict.Deferred) != 0 {
		t.Fatalf("strict must defer nothing, got %q", strict.Deferred)
	}
	joined := strings.Join(strict.Checked, "\n")
	for _, want := range []string{"network none", "Linux container", "executed", "not auto-approved", "built-in definition", "ranks at least strict@1", "isolated in a clone", "allowlisted environment", "Agent Trace"} {
		if !strings.Contains(joined, want) {
			t.Errorf("strict checked clauses lack %q:\n%s", want, joined)
		}
	}

	standard := Conformance(mustProfile(t, "standard"), res)
	standardChecked := strings.Join(standard.Checked, "\n")
	if len(standard.Deferred) != 0 || strings.Contains(standardChecked, "network") ||
		strings.Contains(standardChecked, "allowlisted") || strings.Contains(standardChecked, "Agent Trace") ||
		!strings.Contains(standardChecked, "isolated in a clone") {
		t.Errorf("standard checks isolation but no container, environment or trace clause, and defers nothing: %+v", standard)
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

func TestStrictV2AgainstSignedNotes(t *testing.T) {
	svc, err := New(initRepo(t), "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	p := mustProfile(t, "strict@2")
	for _, scenario := range []string{"valid", "missing", "unbounded", "routes", "cached", "old profile", "tampered", "wrong commit", "untrusted"} {
		t.Run(scenario, func(t *testing.T) {
			rec := strictConformingRecord(t, head)
			ref := domain.NewProfileRef(p)
			rec.Profile = &ref
			rec.PublicKey = base64.StdEncoding.EncodeToString(pub)
			rec.Observations = map[domain.StepName]domain.ContainerObservation{}
			for _, s := range p.RequiredShellSteps(domain.PrePush) {
				rec.Observations[s] = domain.ParseContainerObservation("cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=150000 100000\npids.max=128\nroutes=0\nroutes6=0\n")
			}
			switch scenario {
			case "missing":
				delete(rec.Observations, domain.StepTest)
			case "unbounded":
				v := int64(-1)
				o := rec.Observations[domain.StepTest]
				o.Pids = &v
				rec.Observations[domain.StepTest] = o
			case "routes":
				v := int64(1)
				o := rec.Observations[domain.StepTest]
				o.RoutesIPv6 = &v
				rec.Observations[domain.StepTest] = o
			case "cached":
				rec.StepOutcomes[domain.StepTest] = domain.StepObservation{Outcome: domain.StepCached}
			case "old profile":
				old := domain.NewProfileRef(mustProfile(t, "strict"))
				rec.Profile = &old
			case "wrong commit":
				rec.CommitSHA = "another-commit"
			}
			payload, err := rec.SigningPayload()
			if err != nil {
				t.Fatal(err)
			}
			rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
			if scenario == "tampered" {
				delete(rec.Observations, domain.StepTest)
			}
			if err := svc.Repo().WriteNote(head, rec); err != nil {
				t.Fatal(err)
			}
			key := rec.SignerFingerprint()
			if scenario == "untrusted" {
				key = "untrusted-key"
			}
			res, err := svc.VerifyMinProfile(head, ExternalReject, []domain.ComplianceProfile{p}, key)
			if err != nil {
				t.Fatal(err)
			}
			if res.Validated != (scenario == "valid") {
				t.Fatalf("validated=%v failures=%v", res.Validated, res.RequirementFailures)
			}
		})
	}
}
