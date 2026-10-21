// Package workspace discovers the workspaces of a monorepo — the packages of a
// JS workspace, the modules of a go.work, the members of a Cargo workspace —
// and the dependency edges between them, producing a domain.WorkspaceGraph.
//
// Discovery reads manifests out of one git tree and nothing else. It never
// reads the filesystem and never runs a package manager or the go command, so
// untracked, ignored or step-generated files cannot change the graph, and a
// verifier holding the same git objects rebuilds the same graph.
package workspace

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	gitinfra "go.klarlabs.de/warden/internal/infrastructure/git"
)

// maxManifestBytes caps one manifest. Real package.json, go.mod, go.work and
// Cargo.toml files are kilobytes; the cap refuses a hostile or accidental blob rather
// than parsing it.
const maxManifestBytes = 1 << 20

// Source is a read-only view of one tree: the regular files it holds and
// their committed contents.
type Source interface {
	// Files returns every regular-file path in the tree, '/'-separated,
	// sorted.
	Files() []string
	// Read returns the contents of the named files. A path the tree does not
	// hold as a regular file is absent from the result.
	Read(paths []string) (map[string][]byte, error)
}

// GitTree is a Source backed by the git objects of one revision.
type GitTree struct {
	dir   string
	rev   string
	files []string
}

// OpenGitTree lists the regular files of rev's tree in the repository at dir.
// rev should be a full commit or tree name, so every later read sees the same
// tree even if a ref moves meanwhile.
func OpenGitTree(dir, rev string) (*GitTree, error) {
	var files []string
	// keep sees every regular-file path; returning false means nothing is
	// read yet, so this is a listing only.
	if _, err := gitinfra.ReadTreeBlobs(dir, rev, func(p string) bool {
		files = append(files, p)
		return false
	}); err != nil {
		return nil, fmt.Errorf("list tree %s: %w", rev, err)
	}
	sort.Strings(files)
	return &GitTree{dir: dir, rev: rev, files: files}, nil
}

// Files implements Source.
func (t *GitTree) Files() []string { return t.files }

// Read implements Source.
func (t *GitTree) Read(paths []string) (map[string][]byte, error) {
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	out := make(map[string][]byte, len(paths))
	if len(want) == 0 {
		return out, nil
	}
	blobs, err := gitinfra.ReadTreeBlobs(t.dir, t.rev, func(p string) bool { return want[p] })
	if err != nil {
		return nil, fmt.Errorf("read tree %s: %w", t.rev, err)
	}
	for _, b := range blobs {
		out[b.Path] = b.Content
	}
	return out, nil
}

// readManifests reads the named manifests from src, refusing any larger than
// maxManifestBytes.
func readManifests(src Source, paths []string) (map[string][]byte, error) {
	got, err := src.Read(paths)
	if err != nil {
		return nil, err
	}
	for p, data := range got {
		if len(data) > maxManifestBytes {
			return nil, fmt.Errorf("%s: manifest is larger than %d bytes", p, maxManifestBytes)
		}
	}
	return got, nil
}

// fileSet indexes a Source's file listing.
type fileSet map[string]bool

func newFileSet(src Source) fileSet {
	files := src.Files()
	s := make(fileSet, len(files))
	for _, f := range files {
		s[f] = true
	}
	return s
}

// sortedKeys returns m's keys in order, so iteration over a manifest table is
// deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// joinPath joins a workspace directory and a file name in repository path
// space, where the root directory is ".".
func joinPath(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// discoverer reads the workspaces of one ecosystem from a tree.
type discoverer struct {
	ecosystem string
	what      string
	discover  func(Source, fileSet) ([]domain.Workspace, error)
}

// discoverers lists every ecosystem discovery knows, in the order the labels
// appear in domain.WorkspaceEcosystems.
var discoverers = []discoverer{
	{EcosystemJS, "JS workspaces", discoverJS},
	{EcosystemGo, "go.work modules", discoverGo},
	{EcosystemRust, "Cargo workspace members", discoverCargo},
}

// Discover reads the workspace graph of the tree src describes for the named
// ecosystems (domain.WorkspaceEcosystems; none named means every one): JS
// workspaces (npm, yarn, pnpm), go.work modules and Cargo workspace members.
// A tree that declares none of them yields an empty graph. An ecosystem that
// is not named is not read at all, so a manifest of an ecosystem a repository
// does not discover can neither fail nor change its graph.
//
// The graph is validated before it is returned. A manifest that produces an
// invalid workspace (a name outside the allowed characters, two workspaces at
// one path or under one name) is an error naming the problem; discovery never
// drops a workspace to make the graph valid, because a workspace nobody knows
// about is a workspace nobody checks. Dependency cycles are not invalid and
// are returned as discovered.
func Discover(src Source, ecosystems ...string) (domain.WorkspaceGraph, error) {
	for _, e := range ecosystems {
		if !slices.Contains(domain.WorkspaceEcosystems, e) {
			return domain.WorkspaceGraph{}, fmt.Errorf("unknown ecosystem %q (known: %s)", e, strings.Join(domain.WorkspaceEcosystems, ", "))
		}
	}
	files := newFileSet(src)
	var all []domain.Workspace
	for _, d := range discoverers {
		if len(ecosystems) > 0 && !slices.Contains(ecosystems, d.ecosystem) {
			continue
		}
		found, err := d.discover(src, files)
		if err != nil {
			return domain.WorkspaceGraph{}, fmt.Errorf("discover %s: %w", d.what, err)
		}
		all = append(all, found...)
	}
	g := domain.WorkspaceGraph{Workspaces: all}
	if err := g.Validate(); err != nil {
		return domain.WorkspaceGraph{}, fmt.Errorf("discovered workspaces are invalid: %w", err)
	}
	return g.Canonical(), nil
}

// DiscoverAt reads the workspace graph of rev's tree in the repository at dir
// for the named ecosystems (none named means every one).
func DiscoverAt(dir, rev string, ecosystems ...string) (domain.WorkspaceGraph, error) {
	tree, err := OpenGitTree(dir, rev)
	if err != nil {
		return domain.WorkspaceGraph{}, err
	}
	return Discover(tree, ecosystems...)
}
