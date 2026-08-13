package service

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// ReattestResult reports what Reattest did for one commit.
type ReattestResult struct {
	Target     string // the commit we tried to re-attest
	Source     string // the tree-equal, validated commit its note was carried from ("" if none found)
	Wrote      bool   // a re-attestation note was written
	AlreadyHad bool   // the target already carried a valid note; nothing to do
}

// Reattest gives an un-noted commit a provenance note by carrying over the note
// of an already-validated commit whose tree it EXACTLY reproduces — the
// squash-merge case, where GitHub collapses a gated PR into a new commit id with
// the same content. It is deliberately conservative: it writes a note only when
// it finds a source commit whose tree SHA matches AND whose own note is intact,
// commit-bound, and validly signed. No match → it writes nothing (fail safe): a
// re-attestation never asserts validation that didn't happen, only relocates one
// that did onto content-identical bytes. The new note is re-signed with THIS
// machine's key and marked ReattestedFrom, so it is trusted-signed under the
// roster and transparently a re-attestation.
func (s *Service) Reattest(commitish string, push bool) (ReattestResult, error) {
	_ = s.repo.FetchNotes(s.remote) // best-effort; the source note may live on the remote

	target, err := s.repo.ResolveSHA(commitish)
	if err != nil {
		return ReattestResult{}, fmt.Errorf("resolve %q: %w", commitish, err)
	}
	if rec, _ := s.repo.ReadNote(target); rec != nil && rec.Attests(target) &&
		s.noteHoldsUnderRoster(rec) {
		// Already noted locally — but --push asks for the REMOTE to carry it, and
		// a note written by an earlier push-less run is exactly the case that
		// needs publishing. Returning here without pushing would report success
		// while the note stays local forever.
		if push {
			_ = s.repo.PushNotes(s.remote)
		}
		return ReattestResult{Target: target, AlreadyHad: true}, nil
	}
	if s.signer == nil {
		return ReattestResult{Target: target}, fmt.Errorf("no signing key available to re-attest with")
	}

	targetTree, err := s.repo.TreeSHA(target)
	if err != nil {
		return ReattestResult{}, fmt.Errorf("tree of %s: %w", target, err)
	}
	source, srcRec, err := s.treeEqualSource(target, targetTree, s.reattestTrustSet())
	if err != nil {
		return ReattestResult{}, err
	}
	if source == "" {
		return ReattestResult{Target: target}, nil // fail safe: nothing content-identical is validated
	}

	rec := *srcRec
	rec.CommitSHA = target
	rec.ReattestedFrom = source
	// Drop the source's signature and re-sign as ourselves: the re-attestation is
	// our statement, bound to the target commit.
	rec.PublicKey = s.signer.PublicKey()
	rec.Signature = ""
	payload, err := rec.SigningPayload()
	if err != nil {
		return ReattestResult{}, err
	}
	if rec.Signature, err = s.signer.Sign(payload); err != nil {
		return ReattestResult{}, fmt.Errorf("sign re-attestation: %w", err)
	}

	if err := s.repo.WriteNote(target, rec); err != nil {
		return ReattestResult{}, fmt.Errorf("write re-attestation note: %w", err)
	}
	_ = s.repo.AnchorAttested(target) // keep the evidence reachable (#212 §3)
	if push {
		_ = s.repo.PushNotes(s.remote) // best-effort, mirrors the gate's note push
	}
	return ReattestResult{Target: target, Source: source, Wrote: true}, nil
}

// ReattestAll re-attests every commit on branch since the adoption point whose
// content a validated tree-identical commit already covers. It exists because
// the per-SHA form does not survive contact with reality: a repo can run the
// provenance gate on every PR and still show a majority-unverified base branch,
// because squash-merge mints a new commit id for each merge and nobody
// remembers to relocate the note by hand. One command over the whole gap is what
// makes the repair actually happen.
//
// It is exactly as conservative as Reattest — the same tree-equality and
// trusted-signer rules decide each commit, so this is a batch of the same safe
// operation, never a weaker one. Commits with no validated tree-identical source
// are skipped, not forced. Results come back oldest-first, one per commit
// written; a single push at the end publishes them together.
//
// push means "make the remote match", NOT "publish what this call happened to
// write". Sweeping without --push and then re-running with it is the obvious
// two-step workflow, and gating the push on this invocation having written
// something would silently leave those notes local forever — the run reports
// success while the remote never learns. So push is unconditional whenever it
// is asked for; PushNotes is idempotent and a no-op push costs one cheap
// round trip.
// onProgress, when non-nil, is called before each commit is attempted, with the
// commit and its 1-based position in the sweep. A sweep over a trunk's whole
// backlog is minutes of silence otherwise — the field report watched ~94
// commits produce no output for over ten minutes, which is indistinguishable
// from a hang and is the point most people reach for Ctrl-C.
func (s *Service) ReattestAll(branch string, push bool, onProgress func(sha string, n, total int)) ([]ReattestResult, error) {
	report, err := s.Doctor(branch)
	if err != nil {
		return nil, err
	}
	// Preserve before repairing. Anchoring arrived after most notes were written,
	// so an existing repository is full of attested commits that only a deleted
	// branch still holds; the next gc turns them into dangling notes and the
	// squash commits they could have covered become unrecoverable. Do it first,
	// so a sweep interrupted halfway has still saved what it found.
	s.anchorNotedCommits()

	var out []ReattestResult
	gaps := report.Reattestable()
	for i := range gaps {
		sha := gaps[i].SHA
		if onProgress != nil {
			onProgress(sha, i+1, len(gaps))
		}
		// Push per-commit is suppressed: one push after the batch beats N.
		res, err := s.Reattest(sha, false)
		if err != nil {
			return out, fmt.Errorf("re-attest %s: %w", sha[:min(12, len(sha))], err)
		}
		if res.Wrote {
			out = append(out, res)
		}
	}
	if push {
		_ = s.repo.PushNotes(s.remote) // best-effort, mirrors the gate's note push
	}
	return out, nil
}

// ReattestPlan reports what a sweep WOULD do, writing nothing.
//
// Omitting --push was not a dry run and was widely read as one: it still wrote
// local notes, and the field report watched its note count climb from 222 to
// 301 during what it took to be a preview. A repair tool aimed at a trunk needs
// a way to be asked before it is trusted.
//
// It answers with the same rules the sweep uses — the target is judged by the
// roster, the source must be tree-identical and trusted — so the plan is not an
// approximation of the run, it is the run's own decision made twice.
func (s *Service) ReattestPlan(branch string) ([]ReattestResult, error) {
	report, err := s.Doctor(branch)
	if err != nil {
		return nil, err
	}
	_ = s.repo.FetchNotes(s.remote) // same best-effort fetch Reattest performs

	var out []ReattestResult
	gaps := report.Reattestable()
	for i := range gaps {
		target := gaps[i].SHA
		if rec, _ := s.repo.ReadNote(target); rec != nil && rec.Attests(target) &&
			s.noteHoldsUnderRoster(rec) {
			continue // already holds; the sweep would skip it too
		}
		tree, err := s.repo.TreeSHA(target)
		if err != nil {
			continue
		}
		source, _, err := s.treeEqualSource(target, tree, s.reattestTrustSet())
		if err != nil || source == "" {
			continue // the sweep would write nothing here
		}
		out = append(out, ReattestResult{Target: target, Source: source})
	}
	return out, nil
}

// noteHoldsUnderRoster reports whether an existing note is good enough to leave
// alone, given the roster the repository currently enforces.
//
// Re-attestation used to stop at Attests() — evidence, chain, and commit
// binding — which says nothing about WHO signed. That let an untrusted note
// squat: anything able to write a note to a commit could permanently block a
// trusted one, because reattest saw a "valid" note and declined to replace it.
// The only repair was removing the note and force-pushing the shared notes ref,
// which a protected repository may not allow at all.
//
// The asymmetry was the tell. treeEqualSource already refused to COPY FROM a
// note that was not trusted-signed, while this path refused to REPLACE one. So
// warden was stricter about what it carried over than about what it defended.
//
// Whether the repository enforces at all is decided by the CONFIGURED roster:
// a repository that has not pinned trusted_keys has not opted into judging
// signers, and any attesting note holds there exactly as before.
//
// What counts as trusted, once it does enforce, is reattestTrustSet() — the
// roster PLUS this machine's key. That second term is not laxity, it is
// required for termination: Reattest re-signs with the local key, so in a
// repository whose roster lists some other signer, judging against the roster
// alone would find warden's own re-attestation untrusted and redo it on every
// run, forever.
func (s *Service) noteHoldsUnderRoster(rec *domain.RunRecord) bool {
	cfg, err := s.Config()
	if err != nil || len(cfg.TrustedKeys) == 0 {
		return true
	}
	return rec.VerifySignature() && keyTrusted(rec, s.reattestTrustSet())
}

// reattestTrustSet is the set of signers a re-attestation may carry provenance
// from: the committed roster plus this machine's own key. Carrying over only
// from a trusted (or our own) source stops an untrusted self-signed note — one
// an attacker could push to refs/notes/warden for a tree-identical commit — from
// being laundered into a locally-trusted re-attestation. Our own key is always
// included so the canonical case (re-attesting a squash-merge of a PR head we
// ourselves validated) works even in a repo that pins no roster.
func (s *Service) reattestTrustSet() []string {
	var set []string
	if cfg, err := s.Config(); err == nil {
		set = append(set, cfg.TrustedKeys...)
	}
	if s.signer != nil {
		set = append(set, s.signer.Fingerprint())
	}
	return set
}

// treeEqualSource finds a commit (other than target) whose tree SHA equals
// targetTree and whose warden note is intact, commit-bound, validly signed, AND
// signed by a trusted key — i.e. a genuinely-validated commit with
// byte-identical content whose signer we already trust. It returns the first
// such match, or ("", nil, nil) when none exists.
func (s *Service) treeEqualSource(target, targetTree string, trusted []string) (string, *domain.RunRecord, error) {
	noted, err := s.repo.NotedCommits()
	if err != nil {
		return "", nil, fmt.Errorf("list noted commits: %w", err)
	}
	for _, c := range noted {
		if c == target {
			continue
		}
		tree, err := s.repo.TreeSHA(c)
		if err != nil || tree != targetTree {
			continue
		}
		rec, err := s.repo.ReadNote(c)
		if err != nil || rec == nil {
			continue
		}
		// The source must genuinely attest itself, carry a signature that verifies,
		// AND be signed by a trusted key — otherwise a forged, unsigned, or merely
		// self-signed-but-untrusted note could be laundered into a locally-trusted
		// re-attestation.
		if rec.Attests(c) && rec.VerifySignature() && keyTrusted(rec, trusted) {
			return c, rec, nil
		}
	}
	return "", nil, nil
}

// anchorNotedCommits points an anchor ref at every commit that carries a note
// and still exists, so gc cannot collect the evidence a future re-attestation
// would be carried from (#212 §3).
//
// It is a backfill: the gate anchors as it attests, but anchoring arrived long
// after most notes were written, so an existing repository holds attested
// commits kept alive only by branches that are already scheduled for deletion.
// Commits that are ALREADY gone are skipped rather than reported — their notes
// dangle over nothing and there is no longer anything to preserve.
//
// Wholly best-effort: this runs inside a repair sweep, and failing that sweep
// because a ref could not be written would be a worse outcome than the gap.
func (s *Service) anchorNotedCommits() {
	noted, err := s.repo.NotedCommits()
	if err != nil {
		return
	}
	for _, c := range noted {
		if _, err := s.repo.TreeSHA(c); err != nil {
			continue // object already collected; the note dangles over nothing
		}
		_ = s.repo.AnchorAttested(c)
	}
}
