package signing

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// newSSHKey generates a passphrase-less ed25519 key and returns its private path.
func newSSHKey(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	key := filepath.Join(t.TempDir(), "id")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "warden-test", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("keygen failed: %v %s", err, out)
	}
	return key
}

// The round trip is the contract: what this signer produces must verify under
// the domain's in-process SSHSIG verifier. The two are implemented differently
// on purpose — one shells out, one does not — so only signing with one and
// verifying with the other proves they agree.
func TestSSHSigner_RoundTripsThroughTheDomainVerifier(t *testing.T) {
	s, err := LoadSSH(newSSHKey(t))
	if err != nil {
		t.Fatal(err)
	}
	rec := domain.RunRecord{
		RunID:     "run-1",
		CommitSHA: "abc123",
		PublicKey: s.PublicKey(),
		Algorithm: s.Algorithm(),
	}
	// Sign over the record's own payload, exactly as the runner does.
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = sig

	if !rec.VerifySignature() {
		t.Fatal("a record signed by SSHSigner must verify in the domain")
	}
	// And tampering must still break it.
	rec.CommitSHA = "different"
	if rec.VerifySignature() {
		t.Error("changing a signed field must invalidate the signature")
	}
}

// git stores user.signingkey verbatim, so the configured value is usually the
// .pub path and often contains a literal ~. Both must work, or a value copied
// straight out of git config fails confusingly.
func TestLoadSSH_AcceptsPublicKeyPathAndTilde(t *testing.T) {
	key := newSSHKey(t)

	if _, err := LoadSSH(key + ".pub"); err != nil {
		t.Errorf("a .pub path must be accepted: %v", err)
	}
	if _, err := LoadSSH("  " + key + "  "); err != nil {
		t.Errorf("surrounding whitespace must be tolerated: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("expandHome(~/x) = %q, want it under the home dir", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("an absolute path must be untouched, got %q", got)
	}
	// "~user" is not a form we resolve; leaving it alone is better than
	// guessing at another account's home.
	if got := expandHome("~other/x"); got != "~other/x" {
		t.Errorf("~user must be left alone, got %q", got)
	}
}

// A misconfigured key must fail at construction with a message naming the file,
// not mid-run with a bare ssh-keygen error.
func TestLoadSSH_ReportsMissingKeysClearly(t *testing.T) {
	if _, err := LoadSSH(""); err == nil {
		t.Error("an empty key path must be refused")
	}
	if _, err := LoadSSH(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing private key must be refused")
	}

	// Private key present, public key missing.
	key := newSSHKey(t)
	if err := os.Remove(key + ".pub"); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSSH(key)
	if err == nil || !strings.Contains(err.Error(), ".pub") {
		t.Errorf("a missing public key must be named in the error, got %v", err)
	}
}

func TestSSHSigner_ReportsItsAlgorithm(t *testing.T) {
	s, err := LoadSSH(newSSHKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.Algorithm() != domain.AlgorithmSSH {
		t.Errorf("algorithm = %q, want %q", s.Algorithm(), domain.AlgorithmSSH)
	}
	if !strings.HasPrefix(s.PublicKey(), "ssh-ed25519 ") {
		t.Errorf("public key should be an authorized_keys line, got %q", s.PublicKey())
	}
}

// The namespace itself is asserted here; that a foreign namespace is REJECTED is
// proved in the domain (TestVerifySSHSignature_RejectsAnotherNamespace), which
// is where the verifier lives. Restating it here would only re-test that code.
func TestSSHSigner_UsesAWardenSpecificNamespace(t *testing.T) {
	if domain.SSHSigNamespace == "git" || domain.SSHSigNamespace == "" {
		t.Errorf("namespace = %q; it must be warden-specific so a provenance signature "+
			"cannot be replayed as a git commit signature", domain.SSHSigNamespace)
	}
}
