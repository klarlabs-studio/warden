// Package signing provides warden's ed25519 provenance signer: a per-machine
// keypair, generated on first use and persisted under the user config dir, that
// signs passing pre-push run records so CI can verify not just that a chain is
// intact but that a trusted key produced it (§9).
package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// keyFile is the seed file name under the warden config dir. It holds the
// base64 ed25519 seed (32 bytes) and nothing else, mode 0600.
const keyFile = "signing.key"

// Signer holds a loaded ed25519 keypair and satisfies application.Signer.
type Signer struct {
	priv   ed25519.PrivateKey
	pubB64 string
}

// DefaultDir is warden's per-user config directory (e.g.
// ~/Library/Application Support/warden or ~/.config/warden), where the signing
// key lives. WARDEN_CONFIG_DIR overrides it, chiefly for tests and CI.
func DefaultDir() (string, error) {
	if dir := os.Getenv("WARDEN_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "warden"), nil
}

// Load returns the signer for dir, generating and persisting a fresh keypair on
// first use. The key file is created 0600 inside dir (0700).
func Load(dir string) (*Signer, error) {
	path := filepath.Join(dir, keyFile)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return generate(dir, path)
	case err != nil:
		return nil, fmt.Errorf("read signing key: %w", err)
	}

	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key at %s is malformed", path)
	}
	return fromSeed(ed25519.NewKeyFromSeed(seed)), nil
}

// generate mints a new keypair, persists its seed 0600, and returns the signer.
func generate(dir, path string) (*Signer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if err := os.WriteFile(path, []byte(seed+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write signing key: %w", err)
	}
	return fromSeed(priv), nil
}

func fromSeed(priv ed25519.PrivateKey) *Signer {
	pub := priv.Public().(ed25519.PublicKey)
	return &Signer{priv: priv, pubB64: base64.StdEncoding.EncodeToString(pub)}
}

// PublicKey returns the base64 ed25519 public key that verifies this signer.
func (s *Signer) PublicKey() string { return s.pubB64 }

// Fingerprint is the short, stable identifier to pin as a trusted signer.
func (s *Signer) Fingerprint() string { return domain.KeyFingerprint(s.pubB64) }

// Sign returns a base64 ed25519 signature over payload.
func (s *Signer) Sign(payload []byte) (string, error) {
	sig := ed25519.Sign(s.priv, payload)
	return base64.StdEncoding.EncodeToString(sig), nil
}
