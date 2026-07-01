package service

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// Doctor fetches the target branch and warden notes from origin, then walks
// commits since the adoption point, classifying each into the domain audit
// model: verified (note present, chain checked) or unverified (no note — a
// --no-verify push or an external one). This service only orchestrates I/O; the
// classification and chain verification are domain logic (§9). Note-fetch
// failures are non-fatal: doctor still reports on local state.
func (s *Service) Doctor(branch string) (domain.AuditReport, error) {
	adoption, err := s.repo.ReadAdoption()
	if err != nil {
		return domain.AuditReport{}, err
	}
	if adoption == "" {
		return domain.AuditReport{}, fmt.Errorf("warden was never initialized in this repo (no adoption point); run `warden init`")
	}
	if branch == "" {
		if branch, err = s.repo.CurrentBranch(); err != nil {
			return domain.AuditReport{}, err
		}
	}

	// Best-effort sync; provenance is a side-channel that must not hard-fail.
	_ = s.repo.FetchNotes(s.remote)

	ref := s.remote + "/" + branch
	shas, err := s.repo.CommitsSince(ref, adoption)
	if err != nil {
		// Fall back to the local branch when there is no remote tracking ref.
		if shas, err = s.repo.CommitsSince(branch, adoption); err != nil {
			return domain.AuditReport{}, fmt.Errorf("walk commits since adoption: %w", err)
		}
	}

	report := domain.AuditReport{Adoption: adoption, Branch: branch}
	for _, sha := range shas {
		report.Commits = append(report.Commits, s.classify(sha))
	}
	return report, nil
}

// classify gathers a commit's metadata and note, delegating the verified/intact
// decision to the domain constructor.
func (s *Service) classify(sha string) domain.CommitStatus {
	author, date, subject, _ := s.repo.CommitMeta(sha)
	note, err := s.repo.ReadNote(sha)
	if err != nil {
		note = nil
	}
	return domain.NewCommitStatus(sha, author, date, subject, note)
}
