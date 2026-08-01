package domain

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHSIG verification.
//
// This is implemented in-process rather than by shelling out to
// `ssh-keygen -Y verify`, even though warden shells out to git elsewhere on
// purpose. VerifySignature is called from Attests, which is the fail-closed
// core the whole provenance surface rests on and is used in loops over a
// commit range — a verification that forks a process per commit would make
// `verify --range` unusable on a real branch, and, worse, would make the
// domain's central security predicate depend on a binary being installed.
//
// The format is OpenSSH's SSHSIG (PROTOCOL.sshsig): a "SSHSIG" magic, the
// signer's public key, a namespace, a reserved field, a hash algorithm name,
// and the signature. The signature covers a second blob with the same magic and
// namespace plus the HASH of the message — never the message itself — which is
// what lets a namespace stop a signature made for one purpose being replayed
// as another.

// SSHSigNamespace scopes warden's signatures. A namespace is part of the signed
// blob, so a signature made here cannot be replayed as a git commit signature
// (namespace "git") and vice versa — which matters precisely because warden
// asks people to reuse the key they already sign commits with.
const SSHSigNamespace = "warden-provenance"

// sshSigNamespace is the internal alias used throughout this file.
const sshSigNamespace = SSHSigNamespace

// sshSigMagic prefixes both the container and the signed blob.
const sshSigMagic = "SSHSIG"

// sshSigBlob is the outer container, as stored in RunRecord.Signature (base64).
type sshSigBlob struct {
	Magic         [6]byte
	Version       uint32
	PublicKey     string
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     string
}

// sshSignedData is the inner blob a signature actually covers.
type sshSignedData struct {
	Magic         [6]byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Hash          string
}

// verifySSHSignature reports whether sig is a valid SSHSIG over payload by
// pubKey, in warden's namespace.
//
// It fails closed on every ambiguity: a malformed container, a namespace that
// is not warden's, a hash algorithm it does not implement, or a signing key
// that differs from the one the record names. The last of those is the one that
// matters most — a container carries its own copy of the signer's key, and
// verifying against THAT rather than against the record's would let anyone
// re-sign a record with their own key and have it verify.
func verifySSHSignature(pubKey, sig string, payload []byte) bool {
	key, err := parseSSHPublicKey(pubKey)
	if err != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return false
	}
	var blob sshSigBlob
	if err := ssh.Unmarshal(raw, &blob); err != nil {
		return false
	}
	if string(blob.Magic[:]) != sshSigMagic || blob.Namespace != sshSigNamespace {
		return false
	}
	// The container names its own signer. It must be the key the record claims,
	// or a signature by any key at all would pass.
	containerKey, err := ssh.ParsePublicKey([]byte(blob.PublicKey))
	if err != nil || !keysEqual(containerKey, key) {
		return false
	}

	hashed, ok := hashForSSHSig(blob.HashAlgorithm, payload)
	if !ok {
		return false
	}
	signed := ssh.Marshal(sshSignedData{
		Magic:         blob.Magic,
		Namespace:     blob.Namespace,
		Reserved:      blob.Reserved,
		HashAlgorithm: blob.HashAlgorithm,
		Hash:          string(hashed),
	})

	var parsedSig ssh.Signature
	if err := ssh.Unmarshal([]byte(blob.Signature), &parsedSig); err != nil {
		return false
	}
	return key.Verify(signed, &parsedSig) == nil
}

// hashForSSHSig hashes payload with the algorithm the container names.
//
// Only sha512 is accepted. It is what ssh-keygen uses by default and the only
// one warden produces; accepting a weaker algorithm a verifier merely
// understands would let a signer choose the weakest one warden tolerates.
func hashForSSHSig(algorithm string, payload []byte) ([]byte, bool) {
	if algorithm != "sha512" {
		return nil, false
	}
	sum := sha512.Sum512(payload)
	return sum[:], true
}

// parseSSHPublicKey reads an authorized_keys-style line ("ssh-ed25519 AAAA…
// comment"). The comment is ignored; only type and key material matter.
func parseSSHPublicKey(s string) (ssh.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty ssh public key")
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return nil, err
	}
	return key, nil
}

// keysEqual compares two public keys by their wire encoding, which covers both
// the algorithm and the key material.
func keysEqual(a, b ssh.PublicKey) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}

// SSHKeyFingerprint renders an SSH public key as OpenSSH's own SHA256
// fingerprint ("SHA256:…"), which is the form `ssh-keygen -lf` prints and a
// forge shows next to a registered key — so a roster entry can be checked
// against an identity the operator did not have to transcribe.
func SSHKeyFingerprint(pubKey string) string {
	key, err := parseSSHPublicKey(pubKey)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(key)
}
