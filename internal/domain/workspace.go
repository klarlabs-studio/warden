package domain

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// WorkspaceGraphVersion is the version of the canonical workspace graph
// encoding. It is part of the digest, so changing what the encoding covers (or
// how) must bump it: a verifier comparing graph digests across versions would
// otherwise read two different encodings as two different graphs.
const WorkspaceGraphVersion = 1

// maxWorkspaceNameLen bounds a workspace name. npm caps package names at 214
// bytes; Go module paths run longer, so the cap leaves room for both while
// still refusing absurd input from a manifest a pull request controls.
const maxWorkspaceNameLen = 256

// workspaceNameRe is the allowlist for a workspace name. Names come from
// manifests (package.json "name", a go.mod module path), which the author of a
// pull request controls, and later reach shell commands through placeholders
// such as {workspace.name}. Quoting is the primary defense there; this class is
// the second one. It admits npm names (including "@scope/name") and Go module
// paths, and refuses whitespace, quotes and every shell metacharacter. A name
// must start with an alphanumeric (after an optional "@"), so it can never be
// read as a command-line option, and no "/"-separated element may be empty.
var workspaceNameRe = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._~-]*(?:/[A-Za-z0-9._~-]+)*$`)

// workspaceEcosystemRe is the allowlist for an ecosystem label ("js", "go").
var workspaceEcosystemRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Workspace is one package of a monorepo: a directory that owns the files
// beneath it, under a name its manifest declares.
type Workspace struct {
	// Path is the workspace directory, relative to the repository root, with
	// "/" separators. It is the workspace's identity: dependencies refer to
	// it, and changed paths are attributed by it.
	Path string `json:"id"`
	// Name is what the manifest calls the package (an npm name, a Go module
	// path). It is what per-workspace commands are told, so it is validated
	// against a strict character class.
	Name string `json:"name"`
	// Ecosystem labels which discovery produced the workspace ("js", "go").
	Ecosystem string `json:"ecosystem,omitempty"`
	// Deps are the Paths of the workspaces this one depends on.
	Deps []string `json:"deps"`
}

// WorkspaceGraph is the set of workspaces in one tree and the dependency edges
// between them. The order of Workspaces and of each Deps list carries no
// meaning: Canonical and Digest erase it.
type WorkspaceGraph struct {
	Workspaces []Workspace
}

// canonicalWorkspaceGraph is the digest encoding: versioned, workspaces sorted
// by path, dependencies sorted and de-duplicated.
type canonicalWorkspaceGraph struct {
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
}

// Validate refuses a graph that cannot be selected over safely. A manifest
// that fails here must fail the run with this message; dropping the offending
// workspace would quietly shrink what a check covers.
//
// It refuses: an invalid name or path, two workspaces with the same name or
// path, a dependency on a path that is not a workspace, and a dependency
// cycle. Nested workspaces (including the root, ".") are allowed: a path
// belongs to the workspace with the longest path containing it, the way
// go.work modules and nested JS packages own their own directories.
func (g WorkspaceGraph) Validate() error {
	byPath := make(map[string]Workspace, len(g.Workspaces))
	byName := make(map[string]string, len(g.Workspaces))
	for _, w := range g.Workspaces {
		if err := w.validate(); err != nil {
			return err
		}
		if _, dup := byPath[w.Path]; dup {
			return fmt.Errorf("workspace path %q is declared twice", w.Path)
		}
		if other, dup := byName[w.Name]; dup {
			return fmt.Errorf("workspace name %q is used by both %q and %q", w.Name, other, w.Path)
		}
		byPath[w.Path] = w
		byName[w.Name] = w.Path
	}
	for _, w := range g.Workspaces {
		for _, d := range w.Deps {
			if _, ok := byPath[d]; !ok {
				return fmt.Errorf("workspace %q depends on %q, which is not a workspace", w.Path, d)
			}
		}
	}
	return checkWorkspaceCycles(g.dependencyMap())
}

func (w Workspace) validate() error {
	if err := ValidateWorkspacePath(w.Path); err != nil {
		return err
	}
	if len(w.Name) > maxWorkspaceNameLen || !workspaceNameRe.MatchString(w.Name) {
		return fmt.Errorf("workspace %q has an invalid name %q: names must start with a letter or digit (after an optional @scope/) and use only letters, digits, '.', '_', '~', '-' and '/'", w.Path, w.Name)
	}
	if w.Ecosystem != "" && !workspaceEcosystemRe.MatchString(w.Ecosystem) {
		return fmt.Errorf("workspace %q has an invalid ecosystem %q", w.Path, w.Ecosystem)
	}
	return nil
}

// ValidateWorkspacePath reports why p is not a usable workspace directory:
// it must be the repository root (".") or a clean, relative, "/"-separated
// path below it. ".." elements, backslashes, control characters (NUL and
// newline among them) and elements starting with '-', which a command would
// read as an option, are refused.
func ValidateWorkspacePath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("workspace path is empty")
	case len(p) > maxGlobLen:
		return fmt.Errorf("workspace path is longer than %d bytes", maxGlobLen)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("workspace path %q must be relative to the repository root", p)
	case strings.ContainsRune(p, '\\'):
		return fmt.Errorf("workspace path %q must use '/' separators", p)
	case path.Clean(p) != p:
		return fmt.Errorf("workspace path %q is not clean (want %q)", p, path.Clean(p))
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("workspace path %q contains a control character", p)
		}
	}
	for _, elem := range strings.Split(p, "/") {
		if elem == ".." {
			return fmt.Errorf("workspace path %q leaves the repository", p)
		}
		if strings.HasPrefix(elem, "-") {
			return fmt.Errorf("workspace path %q has an element starting with '-'", p)
		}
	}
	return nil
}

func (g WorkspaceGraph) dependencyMap() map[string][]string {
	deps := make(map[string][]string, len(g.Workspaces))
	for _, w := range g.Workspaces {
		deps[w.Path] = sortedUnique(w.Deps)
	}
	return deps
}

// checkWorkspaceCycles refuses a dependency cycle (a self-dependency is the
// shortest one), naming every workspace on it.
func checkWorkspaceCycles(deps map[string][]string) error {
	const (
		unvisited = iota
		active
		done
	)
	state := make(map[string]int, len(deps))
	var stack []string
	var visit func(string) error
	visit = func(n string) error {
		state[n] = active
		stack = append(stack, n)
		for _, d := range deps[n] {
			switch state[d] {
			case active:
				start := indexOf(stack, d)
				cycle := append(append([]string{}, stack[start:]...), d)
				return fmt.Errorf("workspace dependency cycle: %s", strings.Join(cycle, " -> "))
			case unvisited:
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		return nil
	}
	for _, n := range sortedKeys(deps) {
		if state[n] == unvisited {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// Canonical returns the graph in its canonical order: workspaces sorted by
// path, each dependency list sorted and de-duplicated. Two graphs describing
// the same workspaces and edges have equal canonical forms whatever order
// discovery produced them in.
func (g WorkspaceGraph) Canonical() WorkspaceGraph {
	out := make([]Workspace, 0, len(g.Workspaces))
	for _, w := range g.Workspaces {
		w.Deps = sortedUnique(w.Deps)
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return WorkspaceGraph{Workspaces: out}
}

// Digest validates the graph and returns the digest of its canonical encoding:
// {"version":1,"workspaces":[{"id","name","ecosystem","deps"}…]}. It is
// deterministic and independent of the order of workspaces and dependencies,
// so a verifier rebuilding the graph from the same git objects arrives at the
// same value.
func (g WorkspaceGraph) Digest() (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	return DigestJSON(canonicalWorkspaceGraph{
		Version:    WorkspaceGraphVersion,
		Workspaces: g.Canonical().Workspaces,
	})
}

// Paths returns every workspace path, sorted.
func (g WorkspaceGraph) Paths() []string {
	out := make([]string, 0, len(g.Workspaces))
	for _, w := range g.Workspaces {
		out = append(out, w.Path)
	}
	sort.Strings(out)
	return out
}

// sortedUnique returns a sorted copy of in without duplicates. It never
// returns nil, so a canonical encoding writes [] rather than null.
func sortedUnique(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}
