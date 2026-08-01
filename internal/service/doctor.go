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
	// Span coverage BEFORE reattestation: a commit published by a gated push is
	// not a gap at all, so it should never be offered as something to reattest.
	s.markCovered(&report)
	s.markReattestable(&report)
	return report, nil
}

// markCovered annotates each un-noted commit that a gated push span published.
//
// warden validates ONE tree per run — the tip's — so it deliberately writes no
// note for the intermediate commits of a multi-commit push. It records the span
// instead. `verify --range` has read that back since #86; `doctor` did not, so a
// perfectly ordinary commit-commit-commit-push reported two UNVERIFIED commits
// forever, and anything counting from doctor (notably `fleet status`) reported
// them as BYPASSED — inflating the one number meant to trigger an intervention.
//
// The covering note must itself attest its own commit, exactly as in the range
// gate: a span must not be a cheaper path to "verified" than a note is.
func (s *Service) markCovered(report *domain.AuditReport) {
	gaps := map[string]int{}
	for i := range report.Commits {
		if !report.Commits[i].HasNote {
			gaps[report.Commits[i].SHA] = i
		}
	}
	if len(gaps) == 0 {
		return
	}
	for i := range report.Commits {
		c := &report.Commits[i]
		if !c.HasNote {
			continue // a commit with no note cannot vouch for anything
		}
		rec, err := s.repo.ReadNote(c.SHA)
		if err != nil || rec == nil || rec.CoversFrom == "" || !rec.Attests(c.SHA) {
			continue
		}
		covered, err := s.repo.CommitsInSpan(rec.CoversFrom, c.SHA)
		if err != nil {
			continue
		}
		for _, sha := range covered {
			if j, isGap := gaps[sha]; isGap {
				report.Commits[j].CoveredBy = c.SHA
				delete(gaps, sha)
			}
		}
	}
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

// markReattestable annotates each unverified commit with a validated,
// tree-identical commit that could vouch for it. The overwhelmingly common
// cause of an unverified commit on a base branch is squash-merge: the gated PR
// head and the commit that landed carry the same tree under different ids, so
// the proof exists and is merely unbound. Reporting a bare UNVERIFIED for that
// case reads as "never checked" and is why the gap goes unfixed — naming the
// source turns it into a one-command repair.
//
// It builds the tree index ONCE per audit rather than per commit: the naive
// per-commit search is O(unverified × noted) git invocations, which on a long
// branch is the difference between a snappy doctor and one nobody runs.
// It stays silent (no index, no annotation) when nothing is unverified.
func (s *Service) markReattestable(report *domain.AuditReport) {
	var gaps []int
	for i := range report.Commits {
		if !report.Commits[i].HasNote {
			gaps = append(gaps, i)
		}
	}
	if len(gaps) == 0 {
		return
	}
	index := s.validatedTrees()
	if len(index) == 0 {
		return
	}
	for _, i := range gaps {
		tree, err := s.repo.TreeSHA(report.Commits[i].SHA)
		if err != nil {
			continue
		}
		if src, ok := index[tree]; ok && src != report.Commits[i].SHA {
			report.Commits[i].ReattestableFrom = src
		}
	}
}

// validatedTrees maps tree SHA → a commit whose note genuinely attests it, is
// validly signed, and is signed by a TRUSTED key. It applies exactly the trust
// rule Reattest itself enforces (see treeEqualSource): doctor must not advertise
// a repair that reattest would then refuse, and must never point at an
// untrusted note as if it were provenance. Ties keep the first match; any
// tree-identical validated commit is an equally good source.
func (s *Service) validatedTrees() map[string]string {
	noted, err := s.repo.NotedCommits()
	if err != nil {
		return nil
	}
	trusted := s.reattestTrustSet()
	index := make(map[string]string, len(noted))
	for _, c := range noted {
		rec, err := s.repo.ReadNote(c)
		if err != nil || rec == nil {
			continue
		}
		if !rec.Attests(c) || !rec.VerifySignature() || !keyTrusted(rec, trusted) {
			continue
		}
		tree, err := s.repo.TreeSHA(c)
		if err != nil {
			continue
		}
		if _, dup := index[tree]; !dup {
			index[tree] = c
		}
	}
	return index
}
