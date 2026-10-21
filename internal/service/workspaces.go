package service

import (
	"fmt"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/workspace"
)

// WorkspaceList is the workspace graph of one commit, as `warden workspaces`
// reports it.
type WorkspaceList struct {
	// Commit is the commit whose tree was read.
	Commit string `json:"commit"`
	// GraphDigest is the digest of the canonical graph (domain.Digest).
	GraphDigest string `json:"graph_digest"`
	// Workspaces are in canonical order: sorted by path, deps sorted.
	Workspaces []domain.Workspace `json:"workspaces"`
}

// AffectedWorkspaces is an affected selection between a base and HEAD.
type AffectedWorkspaces struct {
	// Base is the revision the caller named; Anchor is the merge-base of it
	// and HEAD, the commit the change is measured from. Measuring from the
	// merge-base keeps commits that landed on the base after the branch
	// forked out of the change.
	Base   string `json:"base"`
	Anchor string `json:"anchor"`
	Head   string `json:"head"`
	// HeadWorkspaces counts the workspaces discovered at HEAD, the set a
	// full selection is.
	HeadWorkspaces int `json:"head_workspaces"`
	// SelectionDigest is the digest of Selection (domain.Selection.Digest).
	SelectionDigest string           `json:"selection_digest"`
	Selection       domain.Selection `json:"selection"`
}

// Workspaces discovers the workspace graph committed at rev. It reads git
// objects only, so files that are not committed at rev play no part. The
// ecosystems are the ones workspaces.discover names in the configuration
// committed at rev, as a run discovers them; without the block, every
// ecosystem warden knows.
func (s *Service) Workspaces(rev string) (WorkspaceList, error) {
	commit, err := s.resolveCommit(rev)
	if err != nil {
		return WorkspaceList{}, err
	}
	cfg, _, err := s.configurationAt(commit)
	if err != nil {
		return WorkspaceList{}, fmt.Errorf("configuration at %s: %w", shortSHA(commit), err)
	}
	g, err := workspace.DiscoverAt(s.repo.Dir, commit, discoverEcosystems(cfg)...)
	if err != nil {
		return WorkspaceList{}, err
	}
	digest, err := g.Digest()
	if err != nil {
		return WorkspaceList{}, err
	}
	return WorkspaceList{Commit: commit, GraphDigest: digest, Workspaces: g.Workspaces}, nil
}

// AffectedWorkspaces selects the workspaces a change from base to HEAD
// affects, by domain.Select over the committed graphs, the changed paths and
// the effective configuration at both ends. It is read-only: it writes no
// note and runs nothing.
//
// The selection rules (workspaces.global, ignore and unowned) are read from
// the configuration committed at HEAD. Rules that differ at the anchor change
// the configuration digest, which forces a full selection, so the anchor's
// rules never need consulting. An anchor whose configuration cannot be loaded
// counts as an unknown configuration, which forces a full selection rather
// than failing the command. Both graphs are discovered for the ecosystems the
// HEAD configuration names, as a run discovers them.
func (s *Service) AffectedWorkspaces(base string) (AffectedWorkspaces, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return AffectedWorkspaces{}, fmt.Errorf("affected workspaces: invalid base %q", base)
	}
	head, err := s.resolveCommit("HEAD")
	if err != nil {
		return AffectedWorkspaces{}, err
	}
	anchor, err := s.repo.MergeBase(base)
	if err != nil {
		return AffectedWorkspaces{}, fmt.Errorf("no common ancestor of HEAD and %s: %w", base, err)
	}
	headCfg, headConfig, err := s.configurationAt(head)
	if err != nil {
		return AffectedWorkspaces{}, fmt.Errorf("configuration at HEAD: %w", err)
	}
	ecosystems := discoverEcosystems(headCfg)
	baseGraph, err := workspace.DiscoverAt(s.repo.Dir, anchor, ecosystems...)
	if err != nil {
		return AffectedWorkspaces{}, fmt.Errorf("workspaces at the anchor %s: %w", shortSHA(anchor), err)
	}
	headGraph, err := workspace.DiscoverAt(s.repo.Dir, head, ecosystems...)
	if err != nil {
		return AffectedWorkspaces{}, fmt.Errorf("workspaces at HEAD %s: %w", shortSHA(head), err)
	}
	changed, err := s.repo.ChangedPaths(anchor, head)
	if err != nil {
		return AffectedWorkspaces{}, err
	}
	_, anchorConfig, err := s.configurationAt(anchor)
	if err != nil {
		anchorConfig = ""
	}
	sel, err := domain.Select(domain.SelectionInput{
		Base: baseGraph, Head: headGraph, Changed: changed, Rules: headCfg.SelectionRules(),
		AnchorConfigurationDigest: anchorConfig, ConfigurationDigest: headConfig,
	})
	if err != nil {
		return AffectedWorkspaces{}, err
	}
	digest, err := sel.Digest()
	if err != nil {
		return AffectedWorkspaces{}, err
	}
	return AffectedWorkspaces{Base: base, Anchor: anchor, Head: head, HeadWorkspaces: len(headGraph.Workspaces), SelectionDigest: digest, Selection: sel}, nil
}

// discoverEcosystems is what a configuration's workspaces.discover names, or
// nil (every ecosystem) when it names none: `warden workspaces` previews a
// repository that has not configured the block yet.
func discoverEcosystems(cfg domain.Config) []string {
	if cfg.Workspaces == nil {
		return nil
	}
	return cfg.Workspaces.Discover
}

// DefaultAffectedBase is the base `warden workspaces --affected` measures
// from when none is named: the remote's default branch.
func (s *Service) DefaultAffectedBase() (string, error) {
	ref, err := s.repo.DefaultBranchRef(s.remote)
	if err != nil || ref == "" {
		return "", fmt.Errorf("cannot tell the default branch of %s; pass --base", s.remote)
	}
	return ref, nil
}

// configurationAt is the effective configuration committed at rev, extends
// chain included, and its digest — the value policy resolution records as
// ConfigurationDigest.
func (s *Service) configurationAt(rev string) (domain.Config, string, error) {
	cfg, err := config.ResolveAtRef(func(rel string) ([]byte, bool, error) {
		return s.repo.FileAtRef(rev, rel)
	})
	if err != nil {
		return domain.Config{}, "", err
	}
	digest, err := domain.DigestJSON(cfg)
	return cfg, digest, err
}

// resolveCommit resolves rev to a full commit name, refusing a revision git
// would parse as an option.
func (s *Service) resolveCommit(rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("invalid revision %q", rev)
	}
	sha, err := s.repo.ResolveSHA(rev + "^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	return sha, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
