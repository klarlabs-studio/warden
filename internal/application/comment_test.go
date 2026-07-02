package application

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestPRComment(t *testing.T) {
	base := RunResult{
		Outcome: domain.OutcomePassed,
		Policy:  domain.ResolvedPolicy{Steps: []domain.StepName{"test", "lint"}},
		Record:  &domain.RunRecord{RunID: "run_42", PublicKey: "", Signature: ""},
	}

	t.Run("clean run, unsigned", func(t *testing.T) {
		out := prComment(base, "feature/x")
		for _, want := range []string{commentMarker, "Warden gate passed", "`feature/x`", "**2 steps**", "`test`, `lint`", "run_42", "unsigned", "No findings"} {
			if !strings.Contains(out, want) {
				t.Errorf("comment missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("signed run with findings", func(t *testing.T) {
		res := base
		res.Findings = []domain.Finding{
			{Severity: domain.SeverityHigh, File: "auth/token.go", Line: 42, Message: "unchecked error"},
			{Severity: domain.SeverityLow, Message: "no location"},
		}
		// A signed record: fingerprint is derived from the public key; Signed()
		// keys off a non-empty signature.
		res.Record = &domain.RunRecord{RunID: "run_7", PublicKey: signedPubKey, Signature: "c2ln"}

		out := prComment(res, "main")
		if !strings.Contains(out, "signed by `"+domain.KeyFingerprint(signedPubKey)+"`") {
			t.Errorf("expected signer fingerprint line:\n%s", out)
		}
		for _, want := range []string{"**Findings (2):**", "auth/token.go:42", "unchecked error", "[low]"} {
			if !strings.Contains(out, want) {
				t.Errorf("comment missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("single step is not pluralized", func(t *testing.T) {
		res := base
		res.Policy = domain.ResolvedPolicy{Steps: []domain.StepName{"lint"}}
		if out := prComment(res, "b"); !strings.Contains(out, "**1 step**") {
			t.Errorf("expected singular step count:\n%s", out)
		}
	})
}

// signedPubKey is a valid base64 ed25519 public key (32 bytes) for exercising
// the signer-fingerprint rendering without generating one at test time.
const signedPubKey = "xol0IZ6xoITku5FmxybGmtUoka9JwsZ/l9qEb5enqM8="
