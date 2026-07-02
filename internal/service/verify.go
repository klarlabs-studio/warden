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
}

// Verify checks whether a single commit carries an intact warden validation
// note. It is the primitive behind `warden verify` and CI provenance-skip: CI
// can trust a validated commit and skip re-running the checks warden already
// ran. When trustedKeys is non-empty the note must also be signed by one of
// those pinned keys (given as full base64 public keys or fingerprints). Notes
// are fetched best-effort first so a fresh CI checkout sees them.
func (s *Service) Verify(commitish string, trustedKeys ...string) (VerifyResult, error) {
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
		SHA:            sha,
		Validated:      rec.VerifyChain(),
		Record:         rec,
		Signed:         rec.Signed(),
		SignatureValid: rec.VerifySignature(),
		Signer:         rec.SignerFingerprint(),
	}
	if len(trustedKeys) > 0 {
		// A pinned run must chain-verify, signature-verify, and be signed by a
		// trusted key — otherwise it is not validated for provenance-skip.
		res.Trusted = res.SignatureValid && keyTrusted(rec, trustedKeys)
		res.Validated = res.Validated && res.Trusted
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
		if k == rec.PublicKey || k == rec.SignerFingerprint() || domain.KeyFingerprint(k) == rec.SignerFingerprint() {
			return true
		}
	}
	return false
}
