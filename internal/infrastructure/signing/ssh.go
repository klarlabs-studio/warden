package signing

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// SSHSigner signs provenance with the developer's SSH key — the same key git
// signs commits with when gpg.format=ssh.
//
// WHY THIS EXISTS. warden's own key is generated per machine and means nothing
// to anyone else: a trusted_keys roster of warden fingerprints has to be
// hand-maintained, and there is no way to check an entry against any identity
// system. An SSH key is one a forge already publishes and can revoke, so a
// roster entry can be verified against an identity the operator did not have to
// transcribe.
//
// WHY IT SHELLS OUT, when verification deliberately does not. Signing happens
// ONCE per run, so a subprocess costs nothing measurable, and ssh-keygen brings
// agent support, passphrase prompting via the agent, and hardware-backed sk-*
// keys for free — all of which an in-process signer would have to reimplement
// and get right. Verification is the opposite case: it runs per commit over a
// range and is the domain's fail-closed core, so it must not fork or depend on a
// binary being installed (see domain/sshsig.go).
type SSHSigner struct {
	// keyPath is the PRIVATE key. ssh-keygen wants the private key to sign and
	// derives the public one beside it.
	keyPath string
	// pubKey is the authorized_keys line, read once at construction so
	// PublicKey() cannot fail mid-run.
	pubKey string
}

// Algorithm identifies the scheme in the record.
func (s *SSHSigner) Algorithm() string { return domain.AlgorithmSSH }

// PublicKey returns the authorized_keys-style line for the signing key.
func (s *SSHSigner) PublicKey() string { return s.pubKey }

// Fingerprint is OpenSSH's own SHA256 form — the string `ssh-keygen -lf` prints
// and a forge shows beside a registered key, so what `warden key show` displays
// can be compared against an identity the operator did not transcribe.
func (s *SSHSigner) Fingerprint() string { return domain.SSHKeyFingerprint(s.pubKey) }

// LoadSSH prepares an SSH signer for keyPath.
//
// keyPath may name either the private or the public key — people configure
// git's user.signingkey with the .pub path, so accepting both avoids a
// confusing failure for a value copied straight from git config. A leading ~ is
// expanded, because git stores it literally and ssh-keygen will not resolve it.
func LoadSSH(keyPath string) (*SSHSigner, error) {
	priv := strings.TrimSuffix(expandHome(strings.TrimSpace(keyPath)), ".pub")
	if priv == "" {
		return nil, fmt.Errorf("ssh signing: no key configured (set signing.ssh_key, or git's user.signingkey)")
	}
	if _, err := os.Stat(priv); err != nil {
		return nil, fmt.Errorf("ssh signing: private key %s is not readable: %w", priv, err)
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return nil, fmt.Errorf("ssh signing: public key %s.pub is not readable: %w", priv, err)
	}
	line := strings.TrimSpace(string(pub))
	if line == "" {
		return nil, fmt.Errorf("ssh signing: public key %s.pub is empty", priv)
	}
	return &SSHSigner{keyPath: priv, pubKey: line}, nil
}

// Sign produces an SSHSIG over payload in warden's namespace.
//
// The namespace is what stops this signature being replayed as a git commit
// signature, and vice versa — which matters exactly because the same key makes
// both.
func (s *SSHSigner) Sign(payload []byte) (string, error) {
	dir, err := os.MkdirTemp("", "warden-sshsig-")
	if err != nil {
		return "", fmt.Errorf("ssh signing: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	msg := filepath.Join(dir, "payload")
	if err := os.WriteFile(msg, payload, 0o600); err != nil {
		return "", fmt.Errorf("ssh signing: %w", err)
	}
	// -q keeps ssh-keygen's progress chatter out of the gate's output.
	cmd := exec.Command("ssh-keygen", "-q", "-Y", "sign", "-f", s.keyPath, "-n", domain.SSHSigNamespace, msg)
	// Never inherit stdin: an encrypted key with no agent would otherwise stop
	// the gate at an invisible passphrase prompt, mid-push, with no output.
	// Failing with a message the caller can act on is strictly better.
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh signing: ssh-keygen failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	armored, err := os.ReadFile(msg + ".sig")
	if err != nil {
		return "", fmt.Errorf("ssh signing: ssh-keygen wrote no signature: %w", err)
	}
	// ssh-keygen emits PEM armor. Store the raw blob base64'd instead: the record
	// is JSON, and a multi-line armored block would embed newlines in a field
	// every other signer keeps to one line.
	block, _ := pem.Decode(armored)
	if block == nil {
		return "", fmt.Errorf("ssh signing: signature is not PEM-armored")
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

// expandHome resolves a leading ~ against the user's home directory. git stores
// user.signingkey verbatim, so a configured "~/.ssh/id_ed25519.pub" reaches us
// unexpanded and would otherwise be reported as a missing file.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
