package domain

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// EvidenceEntry is one hash-chained evidence record as it appears in a git
// note (§9). It mirrors axi-go's domain.EvidenceRecord projected for storage,
// so `warden doctor` can re-verify the chain without importing the kernel.
type EvidenceEntry struct {
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Hash         string `json:"hash"`
	PreviousHash string `json:"previous_hash,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
}

// DependencyManifest is one dependency lockfile captured in the SBOM: its
// ecosystem (inferred from the filename), repo-relative path, and a SHA-256
// digest of its contents, so a validated commit carries a signed fingerprint of
// its dependency sets.
type DependencyManifest struct {
	Ecosystem string `json:"ecosystem"`
	Path      string `json:"path"`
	Digest    string `json:"digest"`
}

// RunRecord is the payload written to refs/notes/warden for each validated
// commit (§9). It is the tamper-evident provenance a shared branch relies on.
type RunRecord struct {
	RunID         string `json:"run_id"`
	Timestamp     string `json:"timestamp"`
	WardenVersion string `json:"warden_version"`
	// CommitSHA is the commit this record attests. It is part of the signed
	// payload, so a signature binds the provenance to exactly one commit — a
	// signed note cannot be transplanted onto (or replayed against) a different
	// commit. Empty only on legacy pre-binding notes, which then bind to nothing
	// and fail verification (fail-closed). See RunRecord.BindsTo and Service.Verify.
	CommitSHA string `json:"commit_sha,omitempty"`
	// ReattestedFrom, when set, is the SHA of an already-validated commit whose
	// tree this commit exactly reproduces (e.g. the gated PR head a squash-merge
	// collapsed). The evidence below is carried over from that run and this record
	// is re-signed locally, so a re-attestation is transparent: it asserts "the
	// same validated content, under a new commit id" — never a fresh validation.
	ReattestedFrom    string              `json:"reattested_from,omitempty"`
	Agent             map[StepName]string `json:"agent"`
	StepsRun          []StepName          `json:"steps_run"`
	MatchedRules      []string            `json:"matched_rules"`
	EvidenceChainRoot string              `json:"evidence_chain_root"`
	Evidence          []EvidenceEntry     `json:"evidence"`
	// Dependencies is the SBOM: the dependency lockfiles present at validation,
	// each content-digested. Being part of the record, it is covered by the
	// evidence chain and the signature — a signed statement of exactly which
	// dependency sets were in the tree warden gated (§9).
	Dependencies []DependencyManifest `json:"dependencies,omitempty"`
	// PublicKey is the base64 ed25519 public key of the signer (§9). It is
	// covered by Signature, so it cannot be swapped without re-signing.
	PublicKey string `json:"public_key,omitempty"`
	// Signature is the base64 ed25519 signature over the record's SigningPayload.
	// Empty on an unsigned record.
	Signature string `json:"signature,omitempty"`
}

// SigningPayload is the canonical byte string a signature covers: the record
// with the Signature field cleared but PublicKey retained, so the key that
// signed a record is bound into its own signature. encoding/json emits struct
// fields in declaration order and map keys sorted, so the bytes are stable.
func (r RunRecord) SigningPayload() ([]byte, error) {
	r.Signature = "" // value receiver — clears only this copy's field
	return json.Marshal(r)
}

// Signed reports whether the record carries a signature.
func (r RunRecord) Signed() bool { return r.Signature != "" }

// BindsTo reports whether this record attests the given commit. A record with
// an empty CommitSHA (a legacy pre-binding note, or a hand-forged `{}` note)
// binds to no commit and never matches, so unbound notes fail closed. Because
// CommitSHA is inside SigningPayload, a signed record's binding cannot be
// altered without invalidating the signature — this is what makes a signed note
// non-transplantable.
func (r RunRecord) BindsTo(sha string) bool {
	return r.CommitSHA != "" && r.CommitSHA == sha
}

// Attests reports whether the record is a self-consistent, commit-bound
// attestation of sha: its evidence chain is intact and non-empty and it binds
// to sha. This is the minimum bar for `warden verify` to treat a commit as
// validated; pinning a trusted key (Service.Verify with --key) adds
// cryptographic trust on top.
func (r RunRecord) Attests(sha string) bool {
	return len(r.Evidence) > 0 && r.VerifyChain() && r.BindsTo(sha)
}

// VerifySignature reports whether the record's signature is a valid ed25519
// signature over its SigningPayload by the embedded public key. An unsigned or
// malformed record verifies false. This proves integrity and authenticity
// relative to the embedded key; binding that key to a trusted identity is the
// caller's job (pin the fingerprint — see SignerFingerprint).
func (r RunRecord) VerifySignature() bool {
	if r.Signature == "" || r.PublicKey == "" {
		return false
	}
	pub, err := base64.StdEncoding.DecodeString(r.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return false
	}
	payload, err := r.SigningPayload()
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, payload, sig)
}

// SignerFingerprint is a short, stable identifier for the signing key, for
// display and for pinning a trusted signer in CI (`warden verify --key`).
func (r RunRecord) SignerFingerprint() string { return KeyFingerprint(r.PublicKey) }

// KeyFingerprint hashes a base64 ed25519 public key to a 16-hex-char
// fingerprint. An unparseable key yields "".
func KeyFingerprint(publicKeyB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// VerifyChain checks the record's evidence links: the recorded root must equal
// the first entry's hash and every entry's PreviousHash must equal the prior
// entry's hash. This detects reordering, truncation, or a rewritten root
// post-push. Full payload recomputation is intentionally out of scope — the
// note stores link hashes, not step payloads, to stay small (§9 tradeoff).
// This is domain logic: what makes a provenance chain intact is a property of
// the record itself, independent of how it was fetched.
func (r RunRecord) VerifyChain() bool {
	if len(r.Evidence) == 0 {
		return r.EvidenceChainRoot == ""
	}
	if r.EvidenceChainRoot != r.Evidence[0].Hash {
		return false
	}
	for i := 1; i < len(r.Evidence); i++ {
		if r.Evidence[i].PreviousHash != r.Evidence[i-1].Hash {
			return false
		}
	}
	return true
}
