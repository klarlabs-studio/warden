package service

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// CommitStatus is one commit's provenance line in a doctor report.
type CommitStatus struct {
	SHA     string
	Author  string
	Date    string
	Subject string
	// HasNote is true when a refs/notes/warden record exists for the commit.
	HasNote bool
	// ChainIntact reports whether the note's evidence hash-links are internally
	// consistent (root matches, each PreviousHash chains to the prior hash).
	ChainIntact bool
	RunID       string
	Steps       []domain.StepName
}

// DoctorReport summarizes provenance for a branch since adoption (§9).
type DoctorReport struct {
	Adoption string
	Branch   string
	Commits  []CommitStatus
}

// Counts tallies verified/intact/unverified commits for the summary line.
func (r DoctorReport) Counts() (verified, intact, unverified int) {
	for _, c := range r.Commits {
		if c.HasNote {
			verified++
			if c.ChainIntact {
				intact++
			}
		} else {
			unverified++
		}
	}
	return
}

// Doctor fetches the target branch and warden notes from origin, then walks
// commits since the adoption point, classifying each as verified (note present,
// chain checked) or unverified (no note — a --no-verify push or an external
// one). Note-fetch failures are non-fatal: doctor still reports on local state.
func (s *Service) Doctor(branch string) (DoctorReport, error) {
	adoption, err := s.repo.ReadAdoption()
	if err != nil {
		return DoctorReport{}, err
	}
	if adoption == "" {
		return DoctorReport{}, fmt.Errorf("warden was never initialized in this repo (no adoption point); run `warden init`")
	}
	if branch == "" {
		if branch, err = s.repo.CurrentBranch(); err != nil {
			return DoctorReport{}, err
		}
	}

	// Best-effort sync; provenance is a side-channel that must not hard-fail.
	_ = s.repo.FetchNotes(s.remote)

	ref := s.remote + "/" + branch
	shas, err := s.repo.CommitsSince(ref, adoption)
	if err != nil {
		// Fall back to the local branch when there is no remote tracking ref.
		if shas, err = s.repo.CommitsSince(branch, adoption); err != nil {
			return DoctorReport{}, fmt.Errorf("walk commits since adoption: %w", err)
		}
	}

	report := DoctorReport{Adoption: adoption, Branch: branch}
	for _, sha := range shas {
		report.Commits = append(report.Commits, s.classify(sha))
	}
	return report, nil
}

// classify builds the provenance status for one commit.
func (s *Service) classify(sha string) CommitStatus {
	author, date, subject, _ := s.repo.CommitMeta(sha)
	cs := CommitStatus{SHA: sha, Author: author, Date: date, Subject: subject}

	rec, err := s.repo.ReadNote(sha)
	if err != nil || rec == nil {
		return cs
	}
	cs.HasNote = true
	cs.RunID = rec.RunID
	cs.Steps = rec.StepsRun
	cs.ChainIntact = chainIntact(*rec)
	return cs
}

// chainIntact verifies the note's evidence links: the recorded root must equal
// the first entry's hash and every entry's PreviousHash must equal the prior
// entry's hash. This detects reordering, truncation, or a rewritten root
// post-push. Full payload recomputation is intentionally out of scope — the
// note stores link hashes, not step payloads, to stay small (§9 tradeoff).
func chainIntact(rec domain.RunRecord) bool {
	if len(rec.Evidence) == 0 {
		return rec.EvidenceChainRoot == ""
	}
	if rec.EvidenceChainRoot != rec.Evidence[0].Hash {
		return false
	}
	for i := 1; i < len(rec.Evidence); i++ {
		if rec.Evidence[i].PreviousHash != rec.Evidence[i-1].Hash {
			return false
		}
	}
	return true
}
