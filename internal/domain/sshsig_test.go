package domain

import (
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sshSigFixture signs payload with a freshly generated key using the REAL
// ssh-keygen, and returns the public key line and the base64 SSHSIG blob.
//
// The interop is the whole point of this file. warden verifies SSHSIG in-process
// (see sshsig.go for why), so nothing but a signature produced by OpenSSH itself
// proves the parser agrees with the format it claims to implement. A
// hand-rolled fixture would only prove warden agrees with warden.
func sshSigFixture(t *testing.T, payload []byte, namespace string) (pubKey, sig string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "id")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "warden-test", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen keygen failed: %v %s", err, out)
	}
	msg := filepath.Join(dir, "msg")
	if err := os.WriteFile(msg, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-Y", "sign", "-f", key, "-n", namespace, msg).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen sign failed: %v %s", err, out)
	}
	armored, err := os.ReadFile(msg + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(armored)
	if block == nil {
		t.Fatalf("signature is not PEM-armored:\n%s", armored)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pub)), base64.StdEncoding.EncodeToString(block.Bytes)
}

// The load-bearing interop test: a signature OpenSSH produced must verify here.
func TestVerifySSHSignature_AcceptsARealOpenSSHSignature(t *testing.T) {
	payload := []byte(`{"run_id":"run-1","commit_sha":"abc123"}`)
	pub, sig := sshSigFixture(t, payload, sshSigNamespace)

	if !verifySSHSignature(pub, sig, payload) {
		t.Fatal("a signature made by ssh-keygen must verify")
	}
}

func TestVerifySSHSignature_RejectsATamperedPayload(t *testing.T) {
	payload := []byte(`{"run_id":"run-1","commit_sha":"abc123"}`)
	pub, sig := sshSigFixture(t, payload, sshSigNamespace)

	if verifySSHSignature(pub, sig, []byte(`{"run_id":"run-1","commit_sha":"DIFFERENT"}`)) {
		t.Error("a signature must not verify over different bytes")
	}
}

// The namespace is what stops a signature made for one purpose being replayed
// as another. warden asks people to reuse the key they sign COMMITS with, so a
// git-namespaced signature must never satisfy warden's gate.
func TestVerifySSHSignature_RejectsAnotherNamespace(t *testing.T) {
	payload := []byte(`{"run_id":"run-1"}`)
	pub, sig := sshSigFixture(t, payload, "git")

	if verifySSHSignature(pub, sig, payload) {
		t.Error("a signature from the 'git' namespace must not verify as warden provenance")
	}
}

// A container carries its own copy of the signer's key. Verifying against THAT
// rather than the key the record names would let anyone re-sign a record with
// their own key and have it pass.
func TestVerifySSHSignature_RejectsAKeyTheRecordDoesNotName(t *testing.T) {
	payload := []byte(`{"run_id":"run-1"}`)
	_, sig := sshSigFixture(t, payload, sshSigNamespace)
	otherPub, _ := sshSigFixture(t, payload, sshSigNamespace) // a different key

	if verifySSHSignature(otherPub, sig, payload) {
		t.Error("a signature must not verify against a key other than the one that made it")
	}
}

func TestVerifySSHSignature_RejectsMalformedInput(t *testing.T) {
	payload := []byte(`{}`)
	pub, sig := sshSigFixture(t, payload, sshSigNamespace)

	cases := map[string]struct{ pub, sig string }{
		"empty key":       {"", sig},
		"empty signature": {pub, ""},
		"garbage key":     {"not-a-key", sig},
		"garbage sig":     {pub, "!!!not-base64!!!"},
		"truncated sig":   {pub, sig[:len(sig)/2]},
	}
	for name, tc := range cases {
		if verifySSHSignature(tc.pub, tc.sig, payload) {
			t.Errorf("%s must not verify", name)
		}
	}
}

// The fingerprint must match what ssh-keygen prints, or a roster entry copied
// from a forge or from `ssh-keygen -lf` would never match.
func TestSSHKeyFingerprint_MatchesOpenSSH(t *testing.T) {
	payload := []byte(`{}`)
	pub, _ := sshSigFixture(t, payload, sshSigNamespace)

	got := SSHKeyFingerprint(pub)
	if !strings.HasPrefix(got, "SHA256:") {
		t.Fatalf("fingerprint = %q, want OpenSSH's SHA256: form", got)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "k.pub")
	if err := os.WriteFile(p, []byte(pub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ssh-keygen", "-lf", p).Output()
	if err != nil {
		t.Skipf("ssh-keygen -lf failed: %v", err)
	}
	// Output is "<bits> SHA256:<b64> <comment> (<type>)".
	if !strings.Contains(string(out), got) {
		t.Errorf("fingerprint %q not found in ssh-keygen output %q", got, out)
	}
	if SSHKeyFingerprint("nonsense") != "" {
		t.Error("an unparseable key must yield no fingerprint")
	}
}

// SignerFingerprint must dispatch on the algorithm. It originally called the
// ed25519 fingerprint function unconditionally, so an SSH-signed note reported
// an EMPTY signer — verifiable, but impossible to pin or put in a roster, which
// removes the only reason to sign with an SSH key in the first place.
func TestSignerFingerprint_DispatchesOnAlgorithm(t *testing.T) {
	pub, _ := sshSigFixture(t, []byte(`{}`), sshSigNamespace)

	ssh := RunRecord{PublicKey: pub, Algorithm: AlgorithmSSH}
	got := ssh.SignerFingerprint()
	if !strings.HasPrefix(got, "SHA256:") {
		t.Errorf("an SSH-signed record must report OpenSSH's fingerprint, got %q", got)
	}

	// The ed25519 path is unchanged: an SSH key under the default algorithm
	// yields nothing, because it is not a base64 ed25519 key.
	legacy := RunRecord{PublicKey: pub, Algorithm: AlgorithmEd25519}
	if legacy.SignerFingerprint() != "" {
		t.Error("the ed25519 path must not try to fingerprint an SSH key")
	}
}

// A roster must accept the forms a forge actually publishes, or an operator
// cannot name an identity they did not transcribe by hand.
func TestValidTrustedKey_AcceptsSSHForms(t *testing.T) {
	pub, _ := sshSigFixture(t, []byte(`{}`), sshSigNamespace)

	for _, entry := range []string{pub, SSHKeyFingerprint(pub)} {
		if !ValidTrustedKey(entry) {
			t.Errorf("roster entry %q must be accepted", entry[:min(len(entry), 40)])
		}
	}
	if ValidTrustedKey("obviously not a key") {
		t.Error("nonsense must still be rejected")
	}
}
