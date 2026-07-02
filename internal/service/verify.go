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
}

// Verify checks whether a single commit carries an intact warden validation
// note. It is the primitive behind `warden verify` and CI provenance-skip: CI
// can trust a validated commit and skip re-running the checks warden already
// ran. Notes are fetched best-effort first so a fresh CI checkout sees them.
func (s *Service) Verify(commitish string) (VerifyResult, error) {
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
	return VerifyResult{SHA: sha, Validated: rec.VerifyChain(), Record: rec}, nil
}
