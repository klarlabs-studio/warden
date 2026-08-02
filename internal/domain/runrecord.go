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
	ReattestedFrom string `json:"reattested_from,omitempty"`
	// CoversFrom is the commit this push started from: everything in
	// (CoversFrom, CommitSHA] was published by the same gated push. It is part of
	// the signed payload, so the span cannot be widened after the fact.
	//
	// A run validates ONE tree — the tip's, in the worktree. Attesting each
	// commit in a push individually would be a lie, since the intermediate trees
	// were never checked out. But recording only the commit throws away the other
	// half of what happened: a trusted signer ran the policy and vouched for the
	// span it pushed. Without the span, `verify --range` demands per-commit
	// provenance the gate never produces, so a perfectly ordinary
	// commit-commit-commit-push reads as two unverified commits forever (#86).
	CoversFrom        string              `json:"covers_from,omitempty"`
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
	// AgentTrace notarizes an Agent Trace record that was present when this
	// commit was gated. Being inside the record, its digest is covered by the
	// evidence chain AND the signature — which is the whole point: the trace is
	// the agent's own claim about what it wrote, and this is what makes that
	// claim tamper-evident and anchored to a moment when the code was checked.
	//
	// Nil when no trace was configured or found, and omitempty so a note without
	// one stays byte-identical to a note written before this field existed.
	AgentTrace *AgentTraceRef `json:"agent_trace,omitempty"`
	// ExternalRun, when set, names the platform run that executed the checks —
	// warden did not run them itself (ADR 0003). It makes the record's claim
	// WEAKER and says so: "the signer vouches that run X reported these checks
	// passing", not "warden executed these checks".
	//
	// Inside the signed payload, so the reference cannot be attached to or
	// stripped from a signed note. omitempty so a note without one stays
	// byte-identical to a note written before this field existed.
	//
	// An older warden drops this field on unmarshal, recomputes SigningPayload
	// without it, and fails signature verification — so a verifier that pins a
	// signer rejects a claim it cannot understand, by construction. That is why
	// IsExternal records MUST be signed (see Validate).
	ExternalRun *ExternalRunRef `json:"external_run,omitempty"`
	// PublicKey is the signer's public key (§9). Its encoding follows Algorithm:
	// a base64 ed25519 key for warden's own signer, or an authorized_keys-style
	// "ssh-ed25519 AAAA…" line for an SSH signer. It is covered by Signature, so
	// it cannot be swapped without re-signing.
	PublicKey string `json:"public_key,omitempty"`
	// Signature is the signature over the record's SigningPayload. Empty on an
	// unsigned record.
	Signature string `json:"signature,omitempty"`
	// Algorithm names the signature scheme. EMPTY MEANS ed25519 — warden's
	// original and still-default per-machine key.
	//
	// The empty default is load-bearing rather than lazy. This field sits inside
	// SigningPayload, so any non-empty value changes the bytes a signature
	// covers; leaving it empty for the existing scheme keeps every note ever
	// written byte-identical and still verifiable. A new scheme opts in by
	// naming itself.
	Algorithm string `json:"algorithm,omitempty"`
}

// Signature algorithms. AlgorithmEd25519 is the zero value deliberately — see
// RunRecord.Algorithm.
const (
	// AlgorithmEd25519 is warden's own per-machine key, written as "" so notes
	// that predate this field are unchanged.
	AlgorithmEd25519 = ""
	// AlgorithmSSH is an SSHSIG signature from the developer's SSH key — the same
	// key git signs commits with, and one a forge already publishes, so a roster
	// can be checked against an identity someone else maintains.
	AlgorithmSSH = "ssh"
)

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

// Attestation defects, in the order Attests checks them.
const (
	// DefectNoEvidence: the record recorded no steps at all.
	DefectNoEvidence = "no evidence recorded"
	// DefectChainBroken: the hash chain does not link up. This is the one that
	// genuinely suggests the record was altered after it was written.
	DefectChainBroken = "evidence chain broken"
	// DefectUnbound: the record is internally sound but describes a DIFFERENT
	// commit. The ordinary cause is a rewritten history — a rebase or a squash
	// moved the content to a new SHA and the note stayed with the old one. It is
	// not evidence that anyone altered anything.
	DefectUnbound = "note is bound to a different commit"
)

// AttestDefect names why the record fails to attest sha, or "" when it attests.
//
// Attests folds three distinct failures into one boolean, and callers rendered
// that boolean as "TAMPERED". Two of the three have entirely innocent causes,
// and telling someone their history was tampered with because they rebased is
// the same over-accusation as calling an unpushed commit a bypass: it spends the
// reader's trust on a claim the data does not support. The gate is unchanged —
// all three still fail Attests, and verify still refuses them.
func (r RunRecord) AttestDefect(sha string) string {
	switch {
	case len(r.Evidence) == 0:
		return DefectNoEvidence
	case !r.VerifyChain():
		return DefectChainBroken
	case !r.BindsTo(sha):
		return DefectUnbound
	default:
		return ""
	}
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
	payload, err := r.SigningPayload()
	if err != nil {
		return false
	}
	switch r.Algorithm {
	case AlgorithmEd25519:
		return verifyEd25519Signature(r.PublicKey, r.Signature, payload)
	case AlgorithmSSH:
		return verifySSHSignature(r.PublicKey, r.Signature, payload)
	default:
		// An algorithm this warden does not implement verifies FALSE rather than
		// erroring past the check. A newer warden could write a scheme an older
		// one cannot read, and the safe reading of "I don't understand this
		// signature" is "unverified", never "fine".
		return false
	}
}

// verifyEd25519Signature checks warden's own per-machine key scheme.
func verifyEd25519Signature(pubKey, sig string, payload []byte) bool {
	pub, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, payload, raw)
}

// SignerFingerprint is a short, stable identifier for the signing key, for
// display and for pinning a trusted signer in CI (`warden verify --key`).
//
// It dispatches on Algorithm because the two schemes have different key
// encodings AND different fingerprint conventions. An SSH key renders in
// OpenSSH's own "SHA256:…" form deliberately: that is what `ssh-keygen -lf`
// prints and what a forge shows beside a registered key, so a roster entry can
// be checked against an identity nobody had to transcribe — which is the entire
// reason to sign with an SSH key rather than warden's own.
func (r RunRecord) SignerFingerprint() string {
	if r.Algorithm == AlgorithmSSH {
		return SSHKeyFingerprint(r.PublicKey)
	}
	return KeyFingerprint(r.PublicKey)
}

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
