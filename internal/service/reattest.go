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
	if rec, _ := s.repo.ReadNote(target); rec != nil && rec.Attests(target) {
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
	if push {
		_ = s.repo.PushNotes(s.remote) // best-effort, mirrors the gate's note push
	}
	return ReattestResult{Target: target, Source: source, Wrote: true}, nil
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
