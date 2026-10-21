package workspace

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	gitinfra "go.klarlabs.de/warden/internal/infrastructure/git"
)

// RunSource is the application.WorkspaceSource a run's per-workspace steps
// are selected through. Everything it reads comes from git objects in one
// repository.
type RunSource struct {
	repo *gitinfra.Repo
}

// NewRunSource reads from repo.
func NewRunSource(repo *gitinfra.Repo) *RunSource { return &RunSource{repo: repo} }

// TrackedTree implements application.WorkspaceSource.
func (s *RunSource) TrackedTree(dir string) (string, error) { return gitinfra.TrackedTree(dir) }

// Discover implements application.WorkspaceSource.
func (s *RunSource) Discover(rev string, ecosystems []string) (domain.WorkspaceGraph, error) {
	if len(ecosystems) == 0 {
		// A run only discovers what workspaces.discover names, and config
		// validation refuses an empty list; an empty one here is a caller
		// bug, which must not widen into "every ecosystem".
		return domain.WorkspaceGraph{}, fmt.Errorf("discover workspaces: no ecosystems named")
	}
	return DiscoverAt(s.repo.Dir, rev, ecosystems...)
}

// ChangedPaths implements application.WorkspaceSource.
func (s *RunSource) ChangedPaths(from, to string) ([]string, error) {
	return s.repo.ChangedPaths(from, to)
}

// ConfigurationDigestAt implements application.WorkspaceSource: the digest of
// the configuration committed at rev, resolved the way the loader resolves
// the working copy's, so equal configurations digest equally.
func (s *RunSource) ConfigurationDigestAt(rev string) (string, error) {
	cfg, err := config.ResolveAtRef(func(rel string) ([]byte, bool, error) {
		return s.repo.FileAtRef(rev, rel)
	})
	if err != nil {
		return "", err
	}
	return domain.DigestJSON(cfg)
}
