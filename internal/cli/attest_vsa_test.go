package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// gatedResult is a verified note for a commit that genuinely passed the gate.
// The sha is fixed: these tests assert on the SHAPE of the projection, and a
// varying identifier would only obscure which field is being checked.
func gatedResult() service.VerifyResult {
	const sha = "abc123"
	return service.VerifyResult{
		SHA:       sha,
		Validated: true,
		Record: &domain.RunRecord{
			CommitSHA:     sha,
			RunID:         "run-1",
			Timestamp:     "2026-08-01T10:00:00Z",
			WardenVersion: "0.21.0",
		},
	}
}

// The single most important property in this file. Warden does not produce a
// SLSA build level, and a VSA that claimed one would be a false statement in a
// supply-chain artifact — the exact failure the native predicate's own comment
// warns against when it refuses to call itself slsa.dev/provenance.
func TestBuildVSA_NeverClaimsASLSABuildLevel(t *testing.T) {
	res := gatedResult()
	res.SignatureValid = true
	res.Trusted = true

	data, err := json.Marshal(buildVSA(res, "https://githost/org/repo.git", nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SLSA_BUILD_LEVEL") {
		t.Errorf("a warden VSA must never assert a SLSA build level:\n%s", data)
	}
	for _, lvl := range buildVSA(res, "", nil).Predicate.VerifiedLevels {
		if !strings.HasPrefix(lvl, "WARDEN_") {
			t.Errorf("verified level %q is not warden-namespaced", lvl)
		}
	}
}

// Levels accumulate: each one requires the one before it. A note's signature
// proves the note was signed, never that the commit was gated, so a signature
// must not be reportable on its own.
func TestBuildVSA_LevelsAccumulate(t *testing.T) {
	base := gatedResult()

	gated := buildVSA(base, "", nil)
	if got := gated.Predicate.VerifiedLevels; len(got) != 1 || got[0] != levelGated {
		t.Errorf("attested-only levels = %v, want just %s", got, levelGated)
	}

	signed := base
	signed.SignatureValid = true
	if got := buildVSA(signed, "", nil).Predicate.VerifiedLevels; len(got) != 2 {
		t.Errorf("signed levels = %v, want gated+signed", got)
	}

	trusted := signed
	trusted.Trusted = true
	got := buildVSA(trusted, "", nil).Predicate.VerifiedLevels
	if len(got) != 3 || got[2] != levelTrusted {
		t.Errorf("trusted levels = %v, want gated+signed+trusted", got)
	}
}

// An unattested note is FAILED with NO levels. Reporting a level for a commit
// the note does not bind to would launder an unverified commit into a passing
// summary — the fail-closed rule that governs the whole provenance surface.
func TestBuildVSA_UnattestedIsFailedWithNoLevels(t *testing.T) {
	res := service.VerifyResult{
		SHA:       "abc123",
		Validated: false,
		// A signature that verifies, on a note that binds somewhere else.
		SignatureValid: true,
		Trusted:        true,
		Record:         &domain.RunRecord{CommitSHA: "a-different-commit", RunID: "run-1"},
	}
	vsa := buildVSA(res, "", nil)
	if vsa.Predicate.VerificationResult != "FAILED" {
		t.Errorf("verificationResult = %q, want FAILED", vsa.Predicate.VerificationResult)
	}
	if len(vsa.Predicate.VerifiedLevels) != 0 {
		t.Errorf("an unattested note must assert no levels, got %v", vsa.Predicate.VerifiedLevels)
	}
}

// The policy descriptor must not carry a digest warden does not have. A
// fabricated digest is worse than none: it makes the statement look verifiable
// while being uncheckable.
func TestBuildVSA_PolicyHasNoFabricatedDigest(t *testing.T) {
	vsa := buildVSA(gatedResult(), "https://githost/org/repo.git", nil)
	if len(vsa.Predicate.Policy.Digest) != 0 {
		t.Errorf("policy digest must be omitted, got %v", vsa.Predicate.Policy.Digest)
	}
	// It must still identify the policy that ran, pinned to the commit.
	if !strings.Contains(vsa.Predicate.Policy.URI, "abc123") ||
		!strings.HasSuffix(vsa.Predicate.Policy.URI, "#.warden.yaml") {
		t.Errorf("policy uri should pin .warden.yaml at the commit, got %q", vsa.Predicate.Policy.URI)
	}
}

// A local-only repo has no globally resolvable name. Saying so is better than
// inventing a URL.
func TestBuildVSA_NoRemoteFallsBackToCommitIdentity(t *testing.T) {
	vsa := buildVSA(gatedResult(), "", nil)
	if vsa.Predicate.ResourceURI != "git:abc123" {
		t.Errorf("resourceUri = %q, want git:abc123", vsa.Predicate.ResourceURI)
	}
}

// An scp-style remote is not a URL and must not be emitted inside one.
func TestNormalizeRemote(t *testing.T) {
	// The hosts here are deliberately dotless. An scp-style remote is
	// `user@host:path`, which is indistinguishable from an email address to a
	// secret/PII scanner — and this test cannot drop that form, since normalizing
	// it is the whole point. A dotless host keeps the syntax under test intact
	// while not looking like an address.
	cases := map[string]string{
		"git@githost:org/repo.git":          "ssh://githost/org/repo.git",
		"https://githost/org/repo.git":      "https://githost/org/repo.git",
		"ssh://git@githost/org/repo.git":    "ssh://githost/org/repo.git",
		"  https://githost/org/repo.git   ": "https://githost/org/repo.git",
	}
	for in, want := range cases {
		if got := normalizeRemote(in); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A VSA is built to be handed to someone else, and --sign signs it. A CI
// checkout's origin routinely carries a credential, in either half of the
// userinfo, so neither half may survive into the statement. The URIs identify
// the repository; they are not clone commands, and lose nothing by it.
func TestNormalizeRemote_CarriesNoCredential(t *testing.T) {
	cases := map[string]string{
		"https://ci-user:redacted@githost/org/repo.git": "https://githost/org/repo.git",
		"https://ci-token@githost/org/repo.git":         "https://githost/org/repo.git",
		"ssh://ci-user:redacted@githost/org/repo.git":   "ssh://githost/org/repo.git",
	}
	for in, want := range cases {
		got := normalizeRemote(in)
		if got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "@") {
			t.Errorf("normalizeRemote(%q) = %q, which still carries userinfo", in, got)
		}
	}
}

// The credential must not survive into the signed statement either — the unit
// above proves the helper, this proves the field a consumer actually reads.
func TestBuildVSA_ResourceURICarriesNoCredential(t *testing.T) {
	vsa := buildVSA(gatedResult(), "https://ci-user:redacted@githost/org/repo.git", nil)
	for _, uri := range []string{vsa.Predicate.ResourceURI, vsa.Predicate.Policy.URI} {
		if strings.Contains(uri, "redacted") || strings.Contains(uri, "ci-user") {
			t.Errorf("uri = %q, which leaks the remote's credential", uri)
		}
		// These URIs are the `git+<url>@<sha>` form, so a trailing `@` is the
		// commit separator and legitimate. Userinfo is the `@` inside the
		// AUTHORITY — between the scheme and the first path separator — which is
		// the only one that could carry a secret.
		if at := strings.Index(authorityOf(uri), "@"); at >= 0 {
			t.Errorf("uri = %q carries userinfo in its authority", uri)
		}
		if !strings.Contains(uri, "githost/org/repo.git") {
			t.Errorf("uri = %q, want it to still name the repository", uri)
		}
	}
}

// authorityOf returns the host portion of a URI: what sits between "://" and
// the next "/". Empty when the URI has no scheme.
func authorityOf(uri string) string {
	scheme := strings.Index(uri, "://")
	if scheme < 0 {
		return ""
	}
	rest := uri[scheme+3:]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

// The VSA is the interop view; the warden predicate stays the default because
// it is the only one carrying the evidence chain and SBOM that make the note
// verifiable rather than merely assertive.
func TestBuildVSA_IsAdditiveNotAReplacement(t *testing.T) {
	res := gatedResult()
	res.Record.Evidence = []domain.EvidenceEntry{{Kind: "step", Hash: "h1"}}

	native := buildStatement(res)
	if native.PredicateType != wardenPredicateID {
		t.Errorf("the default predicate must stay warden's own, got %q", native.PredicateType)
	}
	if native.Predicate.Evidence == nil {
		t.Error("the warden predicate must keep carrying the evidence chain")
	}
	if got := buildVSA(res, "", nil).PredicateType; got != vsaPredicateID {
		t.Errorf("vsa predicateType = %q, want %q", got, vsaPredicateID)
	}
}

// The summary describes the verification that HAPPENED, so the verifier version
// comes from the signed record rather than from the binary running now.
func TestBuildVSA_VerifierVersionComesFromTheRecord(t *testing.T) {
	vsa := buildVSA(gatedResult(), "", nil)
	if got := vsa.Predicate.Verifier.Version["warden"]; got != "0.21.0" {
		t.Errorf("verifier version = %q, want the record's 0.21.0", got)
	}
}

// An unknown --predicate must be refused, not silently downgraded to the
// default: a caller who asked for one shape and got another would ship the wrong
// statement into their supply chain without being told.
func TestAttest_UnknownPredicateIsRefused(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)
	code, _, errb := run("attest", "--predicate", "slsa-provenance")
	if code != 2 {
		t.Errorf("unknown predicate: code = %d, want 2", code)
	}
	if !strings.Contains(errb, "unknown predicate") {
		t.Errorf("error should name the problem: %q", errb)
	}
}

// SLSA's VSA asks producers to set `subject.annotations.source_refs`, and tells
// consumers to check that an allowed branch appears in it. Without it a consumer
// cannot tell which ref a revision was presented under, which is the substitution
// the field exists to prevent.
func TestVSA_SubjectCarriesSourceRefs(t *testing.T) {
	vsa := buildVSA(gatedResult(), "", []string{"refs/heads/main", "refs/tags/v1.0.0"})

	ann := vsa.Subject[0].Annotations
	if ann == nil {
		t.Fatal("subject carries no annotations; source_refs is where a consumer looks for the ref")
	}
	refs, ok := ann["source_refs"].([]string)
	if !ok {
		t.Fatalf("source_refs is %T, want []string", ann["source_refs"])
	}
	if len(refs) != 2 || refs[0] != "refs/heads/main" || refs[1] != "refs/tags/v1.0.0" {
		t.Errorf("source_refs = %v, want the refs passed in, in order", refs)
	}
}

// A commit with no refs pointing at it is the ordinary case — every commit in
// the middle of a branch looks like that. Emitting `source_refs: []` would say
// "warden looked and found none", which is a different and stronger claim than
// staying silent, and the kind of over-statement this project treats as a defect.
func TestVSA_NoRefsEmitsNoAnnotation(t *testing.T) {
	for _, refs := range [][]string{nil, {}} {
		vsa := buildVSA(gatedResult(), "", refs)
		if vsa.Subject[0].Annotations != nil {
			t.Errorf("refs=%v: annotations = %v, want nil", refs, vsa.Subject[0].Annotations)
		}
		// The absence must survive serialization, not just the struct: a
		// marshaled empty map would still put the key on the wire.
		b, err := json.Marshal(vsa)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(b), "annotations") {
			t.Errorf("refs=%v: serialized statement still carries an annotations key: %s", refs, b)
		}
	}
}
