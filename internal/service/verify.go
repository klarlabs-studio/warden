package service

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// VerifyResult is the outcome of checking one commit's provenance.
type VerifyResult struct {
	SHA       string
	Validated bool // a warden note exists and its evidence chain is intact
	Record    *domain.RunRecord
	// Signed reports whether the note carries a signature; SignatureValid whether
	// that signature verifies against its embedded key; Signer is the signer's
	// fingerprint. Trusted is true when the caller pinned trusted keys and the
	// signature both verifies and was made by one of them.
	Signed         bool
	SignatureValid bool
	Signer         string
	Trusted        bool
	// CarriedTrust records that Trusted was earned by the note's carried original
	// plus tree equality, rather than by the note's own signer. Reported so the
	// difference stays visible: the run that validated this content is somebody
	// else's, on a commit with a different id.
	CarriedTrust bool
	// External reports that the note attests an EXTERNAL run — CI ran the checks
	// and warden recorded the reference (ADR 0003). That claim is weaker than a
	// local attestation, so a caller enforcing a gate is entitled to know which
	// one it got.
	External bool
	// ExternalRefused is set when the note was otherwise sound and was rejected
	// ONLY because it is external and the policy did not allow it. Without it,
	// delivery can say nothing but "unverified", sending someone to look for a
	// missing note that is sitting right there.
	ExternalRefused bool
}

// ExternalPolicy decides whether a verification accepts an external-run
// attestation (ADR 0003).
//
// The zero value REFUSES, deliberately. Every consumer of `warden verify` today
// means "warden ran the checks"; widening that silently on upgrade would weaken
// every gate already deployed, without anyone opting in. Accepting the weaker
// claim has to be a decision somebody made.
type ExternalPolicy int

const (
	// ExternalReject accepts local attestations only. The default.
	ExternalReject ExternalPolicy = iota
	// ExternalAllow accepts either kind.
	ExternalAllow
	// ExternalRequire accepts external attestations only — for a branch whose
	// policy is that CI, not a developer's machine, is the attester of record.
	ExternalRequire
)

// Verify checks whether a single commit carries an intact warden validation
// note. It is the primitive behind `warden verify` and CI provenance-skip: CI
// can trust a validated commit and skip re-running the checks warden already
// ran. When trustedKeys is non-empty the note must also be signed by one of
// those pinned keys (given as full base64 public keys or fingerprints). Notes
// are fetched best-effort first so a fresh CI checkout sees them.
func (s *Service) Verify(commitish string, trustedKeys ...string) (VerifyResult, error) {
	return s.VerifyWithPolicy(commitish, ExternalReject, trustedKeys...)
}

// VerifyWithPolicy is Verify with an explicit decision about external-run
// attestations. Verify itself keeps the strict default, so every existing caller
// means exactly what it meant before this existed.
func (s *Service) VerifyWithPolicy(commitish string, policy ExternalPolicy, trustedKeys ...string) (VerifyResult, error) {
	_ = s.repo.FetchNotes(s.remote) // best-effort; provenance is a side-channel

	sha, err := s.repo.ResolveSHA(commitish)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("resolve %q: %w", commitish, err)
	}
	rec, err := s.repo.ReadNote(sha)
	if err != nil {
		return VerifyResult{}, err
	}
	if rec == nil {
		return VerifyResult{SHA: sha}, nil
	}

	res := VerifyResult{
		SHA: sha,
		// Attests requires an intact, non-empty evidence chain AND that the record
		// binds to this exact commit (rec.CommitSHA == sha). This closes two forgeries
		// the bare chain check allowed: an empty `{}` note (binds to nothing) and a
		// signed note transplanted from another commit (binds to that commit, not this).
		Validated:      rec.Attests(sha),
		Record:         rec,
		Signed:         rec.Signed(),
		SignatureValid: rec.VerifySignature(),
		Signer:         rec.SignerFingerprint(),
	}
	if len(trustedKeys) > 0 {
		// A pinned run must attest this commit, signature-verify, and be signed by a
		// trusted key — otherwise it is not validated for provenance-skip.
		res.Trusted = res.SignatureValid && keyTrusted(rec, trustedKeys)
		// …unless it is a re-attestation carrying the original. Then the trust
		// comes from the carried record and the trees, not from whoever relocated
		// it, so an untrusted (or absent) local signature is not disqualifying.
		if !res.Trusted && s.carriedOriginalHolds(sha, rec, trustedKeys) {
			res.Trusted = true
			res.CarriedTrust = true
		}
		res.Validated = res.Validated && res.Trusted
	}

	// The external-run policy is applied LAST, over an otherwise-decided result,
	// so "this note is fine but you did not ask for this kind" stays reportable
	// as its own thing rather than collapsing into a bare "unverified".
	res.External = rec.IsExternal()
	switch {
	case res.External && policy == ExternalReject:
		res.ExternalRefused = res.Validated
		res.Validated = false
	case res.External && res.Validated:
		// An external record that reaches here must still satisfy its own
		// invariants: bound to THIS commit, and signed. A note that fails them was
		// written by something that did not go through warden's writer, so the
		// safe reading is that it is not an attestation at all.
		if err := rec.ValidateExternal(); err != nil {
			res.Validated = false
		}
	case !res.External && policy == ExternalRequire:
		res.ExternalRefused = res.Validated
		res.Validated = false
	}
	return res, nil
}

// keyTrusted reports whether the record's signer matches any pinned key, given
// either as a full base64 public key or as a fingerprint.
func keyTrusted(rec *domain.RunRecord, trustedKeys []string) bool {
	for _, k := range trustedKeys {
		if k == "" {
			continue
		}
		// A roster entry may be the key itself or its fingerprint, in either
		// scheme's encoding. Matching all the forms means an operator can paste
		// whichever one they have — an ed25519 fingerprint from `warden key
		// show`, or an SSH key or SHA256 fingerprint straight off a forge —
		// without knowing which scheme the note happens to use.
		if k == rec.PublicKey || k == rec.SignerFingerprint() ||
			domain.KeyFingerprint(k) == rec.SignerFingerprint() ||
			domain.SSHKeyFingerprint(k) == rec.SignerFingerprint() {
			return true
		}
	}
	return false
}

// carriedOriginalHolds decides whether a re-attestation is trustworthy on the
// strength of the record it carries, rather than on who signed the note.
//
// Re-attestation used to re-sign with the local key, which meant the repair
// could only be done by a machine already on the roster. A merge-time CI job —
// the one place the repair can happen automatically, because it is the only
// moment the squash commit and the source branch both exist — has an ephemeral
// key, so its notes were correctly rejected by the repo's own roster (#212 §7).
//
// The way out is that a re-attestation makes a claim nobody needs to be trusted
// for. All three parts are checkable:
//
//  1. the carried record is validly signed by a TRUSTED key, and attests the
//     source commit — this is the original validation, untouched;
//  2. the source and target commits have IDENTICAL TREES, read from git objects
//     rather than asserted by the note;
//  3. the note under inspection is bound to the target.
//
// Together those say: this exact content was validated by a run we trust. The
// re-attester contributes only the pointer, and a wrong pointer fails (2).
//
// Both commits must still exist for (2) — which is why attested commits are
// anchored (#212 §3). A pruned source is not a verification failure to paper
// over: without the tree comparison there is nothing here but a claim.
func (s *Service) carriedOriginalHolds(target string, rec *domain.RunRecord, trustedKeys []string) bool {
	if rec == nil || rec.ReattestedFrom == "" || rec.CarriedOriginal == nil {
		return false
	}
	orig := rec.CarriedOriginal
	// The carried record must be the real thing: bound to the source commit,
	// chain intact, signature valid, signer on the roster. Anything less and this
	// is just a note claiming to quote one.
	if !orig.Attests(rec.ReattestedFrom) || !orig.VerifySignature() || !keyTrusted(orig, trustedKeys) {
		return false
	}
	// And the content must actually be the same content. This is the step that
	// makes the re-attester's honesty irrelevant.
	srcTree, err := s.repo.TreeSHA(rec.ReattestedFrom)
	if err != nil {
		return false
	}
	tgtTree, err := s.repo.TreeSHA(target)
	if err != nil || srcTree != tgtTree {
		return false
	}
	return true
}
