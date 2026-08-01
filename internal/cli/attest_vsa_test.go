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

	data, err := json.Marshal(buildVSA(res, "https://githost/org/repo.git"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SLSA_BUILD_LEVEL") {
		t.Errorf("a warden VSA must never assert a SLSA build level:\n%s", data)
	}
	for _, lvl := range buildVSA(res, "").Predicate.VerifiedLevels {
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

	gated := buildVSA(base, "")
	if got := gated.Predicate.VerifiedLevels; len(got) != 1 || got[0] != levelGated {
		t.Errorf("attested-only levels = %v, want just %s", got, levelGated)
	}

	signed := base
	signed.SignatureValid = true
	if got := buildVSA(signed, "").Predicate.VerifiedLevels; len(got) != 2 {
		t.Errorf("signed levels = %v, want gated+signed", got)
	}

	trusted := signed
	trusted.Trusted = true
	got := buildVSA(trusted, "").Predicate.VerifiedLevels
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
	vsa := buildVSA(res, "")
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
	vsa := buildVSA(gatedResult(), "https://githost/org/repo.git")
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
	vsa := buildVSA(gatedResult(), "")
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
		"git@githost:org/repo.git":          "ssh://git@githost/org/repo.git",
		"https://githost/org/repo.git":      "https://githost/org/repo.git",
		"ssh://git@githost/org/repo.git":    "ssh://git@githost/org/repo.git",
		"  https://githost/org/repo.git   ": "https://githost/org/repo.git",
	}
	for in, want := range cases {
		if got := normalizeRemote(in); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
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
	if got := buildVSA(res, "").PredicateType; got != vsaPredicateID {
		t.Errorf("vsa predicateType = %q, want %q", got, vsaPredicateID)
	}
}

// The summary describes the verification that HAPPENED, so the verifier version
// comes from the signed record rather than from the binary running now.
func TestBuildVSA_VerifierVersionComesFromTheRecord(t *testing.T) {
	vsa := buildVSA(gatedResult(), "")
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
