package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestLoad_GeneratesPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()

	s1, err := Load(dir)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if s1.PublicKey() == "" || s1.Fingerprint() == "" {
		t.Fatal("generated signer must expose a key and fingerprint")
	}

	// The key file is created 0600.
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}

	// Reloading the same dir yields the same identity, not a new key.
	s2, err := Load(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.PublicKey() != s1.PublicKey() {
		t.Error("reloading must return the persisted key, not generate a new one")
	}
}

func TestSigner_SignVerifiesAgainstPublicKey(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"run_id":"run_1"}`)

	sigB64, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(s.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Error("signature must verify against the signer's public key")
	}
	// Fingerprint matches the domain's derivation, so `key show` and `verify
	// --key` agree.
	if s.Fingerprint() != domain.KeyFingerprint(s.PublicKey()) {
		t.Error("signer fingerprint must match domain.KeyFingerprint")
	}
}

func TestLoad_MalformedKeyErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("not-a-valid-seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("a malformed key file must error, not silently regenerate")
	}
}
