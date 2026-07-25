package service

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// RangeVerifyOptions tunes how strict a range gate is. The zero value is the
// lenient default that matches today's single-commit verify: a note must attest
// each commit, but an attested-yet-unsigned note is allowed, so upgrading does
// not suddenly fail a repo that never signed. RequireSigned adds "the signature
// must verify"; TrustedKeys additionally pins "…and be one of these keys".
type RangeVerifyOptions struct {
	RequireSigned bool
	// TrustedKeys pins the trusted signer set explicitly (an out-of-band anchor).
	// When set it wins outright. When empty and UseRoster is true, VerifyRange
	// reads the roster from the BASE ref itself (see below).
	TrustedKeys []string
	// UseRoster asks VerifyRange to resolve the trusted-signer roster from the
	// range's BASE ref — the trusted side — when no explicit TrustedKeys are
	// pinned. This invariant ("a range gate never reads trust from the head it is
	// checking") lives in the application core so every delivery surface inherits
	// it, rather than each adapter re-deriving it.
	UseRoster  bool
	SkipMerges bool
}

// RangeVerifyResult is the per-commit outcome of a `base..head` gate.
type RangeVerifyResult struct {
	Base    string
	Head    string
	Commits []domain.CommitVerdict
	// Effective is the options actually enforced, with TrustedKeys resolved from
	// the base ref when UseRoster requested it — so a caller can report the true
	// gate depth without re-deriving it. RosterFromBase records that a base-ref
	// roster supplied those keys.
	Effective      RangeVerifyOptions
	RosterFromBase bool
}

// OK reports whether every commit in the range passed the gate. An empty range
// (nothing between base and head) is trivially OK.
func (r RangeVerifyResult) OK() bool {
	for _, c := range r.Commits {
		if !c.OK() {
			return false
		}
	}
	return true
}

// Failures returns the commits that did not pass, in range order.
func (r RangeVerifyResult) Failures() []domain.CommitVerdict {
	var out []domain.CommitVerdict
	for _, c := range r.Commits {
		if !c.OK() {
			out = append(out, c)
		}
	}
	return out
}

// VerifyRange gates every commit in base..head: each must carry a note that
// attests it, and — under RequireSigned / TrustedKeys — a valid, trusted
// signature. Unlike doctor (which flags only *missing* notes), a broken,
// transplanted, or untrusted note fails here. It is a pure read over
// refs/notes/warden: no push or signing path is touched. Notes are fetched
// best-effort first so a fresh CI checkout sees them.
func (s *Service) VerifyRange(base, head string, opts RangeVerifyOptions) (RangeVerifyResult, error) {
	_ = s.repo.FetchNotes(s.remote) // best-effort; provenance is a side-channel

	baseSHA, err := s.repo.ResolveSHA(base)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("resolve base %q: %w", base, err)
	}
	headSHA, err := s.repo.ResolveSHA(head)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("resolve head %q: %w", head, err)
	}

	// Establish the trusted-signer roster from the BASE ref (the trusted side)
	// unless the caller pinned explicit keys. Resolving it here keeps the
	// invariant in the application core: a range gate can never be tricked into
	// trusting a roster the head under gate supplied. A malformed base roster is
	// fail-closed — we refuse rather than silently drop to a weaker depth.
	rosterFromBase := false
	if len(opts.TrustedKeys) == 0 && opts.UseRoster {
		roster, rerr := s.TrustedKeysAt(baseSHA)
		if rerr != nil {
			return RangeVerifyResult{}, fmt.Errorf("read trusted roster at base %s: %w", base, rerr)
		}
		if len(roster) > 0 {
			opts.TrustedKeys = roster
			rosterFromBase = true
		}
	}

	shas, err := s.repo.CommitsInRange(baseSHA, headSHA, opts.SkipMerges)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("walk %s..%s: %w", base, head, err)
	}

	res := RangeVerifyResult{Base: baseSHA, Head: headSHA, Effective: opts, RosterFromBase: rosterFromBase}
	for _, sha := range shas {
		res.Commits = append(res.Commits, s.verdictFor(sha, opts))
	}
	return res, nil
}

// verdictFor classifies one commit against the gate's strictness. The order of
// checks is deliberate: absence, then integrity, then signature, then trust —
// so the reported reason is the *first* thing wrong, the most actionable one.
func (s *Service) verdictFor(sha string, opts RangeVerifyOptions) domain.CommitVerdict {
	rec, err := s.repo.ReadNote(sha)
	if err != nil || rec == nil {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonMissing}
	}
	if !rec.Attests(sha) {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonBrokenChain}
	}
	// A trusted-key requirement implies the signature must first verify.
	if opts.RequireSigned || len(opts.TrustedKeys) > 0 {
		if !rec.VerifySignature() {
			return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonUnsigned}
		}
	}
	if len(opts.TrustedKeys) > 0 && !keyTrusted(rec, opts.TrustedKeys) {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonUntrusted}
	}
	return domain.CommitVerdict{SHA: sha}
}
