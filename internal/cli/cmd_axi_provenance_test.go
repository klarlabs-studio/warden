package cli

import (
	"strings"
	"testing"
)

// The provenance verbs are pure reads, so they must answer WITHOUT the trust
// opt-in that run-trigger requires. That checkpoint exists to stop arbitrary
// shell from an untrusted .warden.yaml; reading a note cannot do that, and
// gating the reads behind it would leave an agent unable to ask the one question
// warden exists to answer.
func TestAxi_ProvenanceVerbsNeedNoTrust(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir()) // keep the signing key out of the developer's real config
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	// doctor/audit report coverage SINCE ADOPTION, so the repo has to have been
	// adopted for the question to mean anything.
	if code, _, errb := run("init"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}

	// status reports the gate's installed state.
	code, out, errb := run("axi", "status")
	if code != 0 {
		t.Fatalf("axi status: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"version", "installed_hooks", "pre_commit", "pre_push"} {
		if !strings.Contains(out, want) {
			t.Errorf("axi status missing %q: %q", want, out)
		}
	}

	// doctor and audit report the same schema so an agent learns one shape.
	for _, verb := range []string{"doctor", "audit"} {
		code, out, errb := run("axi", verb)
		if code != 0 {
			t.Fatalf("axi %s: code=%d err=%q", verb, code, errb)
		}
		for _, want := range []string{"verified", "unverified", "reattestable"} {
			if !strings.Contains(out, want) {
				t.Errorf("axi %s missing %q: %q", verb, want, out)
			}
		}
	}
}

// verify is a GATE, so its verdict must be in the exit status too — a gate that
// always exits 0 gates nothing. An un-gated commit (this repo was never pushed
// through warden) is the unvalidated case.
func TestAxi_VerifyExitsNonZeroWhenNotValidated(t *testing.T) {
	gitRepo(t)

	code, out, _ := run("axi", "verify")
	if code == 0 {
		t.Errorf("verify on an un-gated commit must exit non-zero, got 0: %q", out)
	}
	// The payload must still be emitted: the caller gets the reason, not just a
	// bare failure.
	if !strings.Contains(out, "validated") {
		t.Errorf("verify must emit its verdict even when failing: %q", out)
	}
}

// verify-range is the PR-check shape: it fails when any commit in the span lacks
// provenance, and names the base it gated against.
func TestAxi_VerifyRangeGatesTheSpan(t *testing.T) {
	gitRepo(t)

	// --base is the one required argument; without it the verb is a usage error,
	// not a silently-empty pass.
	if code, _, errb := run("axi", "verify-range"); code != 2 || !strings.Contains(errb, "--base is required") {
		t.Errorf("verify-range without --base: code=%d err=%q", code, errb)
	}

	code, out, _ := run("axi", "verify-range", "--base", "HEAD")
	// An empty range (HEAD..HEAD) is trivially OK — nothing unproven in it.
	if code != 0 {
		t.Errorf("an empty range must pass, got code=%d: %q", code, out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("verify-range output shape: %q", out)
	}
}

func TestAxi_UnknownVerbIsAUsageError(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("axi", "no-such-verb"); code != 2 || !strings.Contains(errb, "unknown verb") {
		t.Errorf("unknown verb: code=%d err=%q", code, errb)
	}
	// The usage line must advertise the provenance verbs, or an agent reading it
	// never learns they exist.
	if _, _, errb := run("axi"); !strings.Contains(errb, "verify") {
		t.Errorf("usage must list the provenance verbs: %q", errb)
	}
}
