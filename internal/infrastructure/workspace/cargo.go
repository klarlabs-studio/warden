package workspace

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.klarlabs.de/warden/internal/domain"
)

// EcosystemRust labels the members of a Cargo workspace.
const EcosystemRust = "rust"

const cargoToml = "Cargo.toml"

// cargoDepTables are the tables of one manifest (or one [target.<cfg>]
// table) whose entries name crates a member depends on. Every kind counts, as
// for JS: a dev- or build-dependency on a sibling still means the sibling's
// change can break this member's checks. The underscore spellings are older
// aliases Cargo still reads.
type cargoDepTables struct {
	Dependencies            map[string]any `toml:"dependencies"`
	DevDependencies         map[string]any `toml:"dev-dependencies"`
	LegacyDevDependencies   map[string]any `toml:"dev_dependencies"`
	BuildDependencies       map[string]any `toml:"build-dependencies"`
	LegacyBuildDependencies map[string]any `toml:"build_dependencies"`
}

func (t cargoDepTables) all() []map[string]any {
	return []map[string]any{t.Dependencies, t.DevDependencies, t.LegacyDevDependencies, t.BuildDependencies, t.LegacyBuildDependencies}
}

// cargoManifest is the part of a Cargo.toml discovery reads.
type cargoManifest struct {
	Package *struct {
		Name *string `toml:"name"`
	} `toml:"package"`
	Workspace *struct {
		Members      []string       `toml:"members"`
		Exclude      []string       `toml:"exclude"`
		Dependencies map[string]any `toml:"dependencies"`
	} `toml:"workspace"`

	Dependencies            map[string]any            `toml:"dependencies"`
	DevDependencies         map[string]any            `toml:"dev-dependencies"`
	LegacyDevDependencies   map[string]any            `toml:"dev_dependencies"`
	BuildDependencies       map[string]any            `toml:"build-dependencies"`
	LegacyBuildDependencies map[string]any            `toml:"build_dependencies"`
	Target                  map[string]cargoDepTables `toml:"target"`

	Patch   map[string]map[string]any `toml:"patch"`
	Replace map[string]any            `toml:"replace"`
}

// depTables returns every dependency table of the manifest, target-specific
// ones included, in a stable order.
func (m cargoManifest) depTables() []map[string]any {
	out := cargoDepTables{
		Dependencies: m.Dependencies, DevDependencies: m.DevDependencies, LegacyDevDependencies: m.LegacyDevDependencies,
		BuildDependencies: m.BuildDependencies, LegacyBuildDependencies: m.LegacyBuildDependencies,
	}.all()
	for _, cfg := range sortedKeys(m.Target) {
		out = append(out, m.Target[cfg].all()...)
	}
	return out
}

func parseCargo(file string, data []byte) (cargoManifest, error) {
	var m cargoManifest
	if err := toml.Unmarshal(trimBOM(data), &m); err != nil {
		return cargoManifest{}, fmt.Errorf("%s: %w", file, err)
	}
	return m, nil
}

// cargoDep is one dependency entry, reduced to what can link two members.
type cargoDep struct {
	key string
	// crate is the crate the entry resolves to by name: its `package` when
	// renamed, otherwise its key.
	crate string
	// path is the directory a `path =` points at, resolved against the
	// manifest that declares it (the root for an inherited entry); "" for
	// none, and for a path that leaves the repository, which links nothing.
	path string
	// raw is the path as the manifest wrote it, for messages.
	raw string
}

// cargoWorkspace is the root manifest's [workspace] plus the root tables that
// apply to every member.
type cargoWorkspace struct {
	include, exclude []string
	// literal are the members named without a glob. Cargo never excludes a
	// crate at or below one of them.
	literal []string
	// inherited are the [workspace.dependencies] entries a member's
	// `workspace = true` refers to.
	inherited map[string]any
	// patched maps a crate name that root [patch.<source>] or [replace]
	// points at a directory to that directory.
	patched map[string][]string
}

// discoverCargo returns the members of the Cargo workspace whose root is the
// repository root: the crates a root Cargo.toml's [workspace] `members` globs
// match (each a directory holding a Cargo.toml), the root package itself when
// the manifest has one, and — as Cargo does — every crate inside the
// repository that a member reaches by a path dependency. An `exclude` entry
// removes the crates at and below it, except those at or below a member named
// without a glob. A Cargo.toml without [workspace] is a single crate, not a
// monorepo; a workspace rooted below the repository root is not discovered.
//
// A member depends on another when one of its dependency tables (normal, dev,
// build, any target) names it: by a `path`, by `workspace = true` resolving to
// a [workspace.dependencies] entry with a `path`, or by a crate name the root
// [patch] or [replace] points at the other member's directory. Every such
// reading adds an edge; none removes one. A path that leaves the repository,
// or names an excluded crate, links nothing.
func discoverCargo(src Source, files fileSet) ([]domain.Workspace, error) {
	if !files[cargoToml] {
		return nil, nil
	}
	data, err := readManifests(src, []string{cargoToml})
	if err != nil {
		return nil, err
	}
	root, err := parseCargo(cargoToml, data[cargoToml])
	if err != nil {
		return nil, err
	}
	if root.Workspace == nil {
		return nil, nil
	}
	cw, err := newCargoWorkspace(root)
	if err != nil {
		return nil, err
	}

	manifests := map[string]cargoManifest{}
	var fresh []string // members read in this round, whose path dependencies are next
	if root.Package != nil {
		manifests["."] = root
		fresh = append(fresh, ".")
	}
	pending, err := cw.memberDirs(src, files)
	if err != nil {
		return nil, err
	}
	// Members are read in rounds: a round's path dependencies may name crates
	// no glob matched, which Cargo makes members too.
	for len(pending) > 0 || len(fresh) > 0 {
		paths := make([]string, len(pending))
		for i, dir := range pending {
			paths[i] = joinPath(dir, cargoToml)
		}
		got, err := readManifests(src, paths)
		if err != nil {
			return nil, err
		}
		for _, dir := range pending {
			file := joinPath(dir, cargoToml)
			m, err := parseCargo(file, got[file])
			if err != nil {
				return nil, err
			}
			manifests[dir] = m
			fresh = append(fresh, dir)
		}
		pending = nil
		for _, dir := range fresh {
			found, err := cw.implicitMembers(dir, manifests[dir], files, manifests, pending)
			if err != nil {
				return nil, err
			}
			pending = append(pending, found...)
		}
		fresh = nil
	}

	out := make([]domain.Workspace, 0, len(manifests))
	for _, dir := range sortedKeys(manifests) {
		m := manifests[dir]
		file := joinPath(dir, cargoToml)
		if m.Package == nil || m.Package.Name == nil || *m.Package.Name == "" {
			return nil, fmt.Errorf("%s: a workspace member needs [package] name", file)
		}
		deps, err := cw.deps(dir, m)
		if err != nil {
			return nil, err
		}
		var edges []string
		for _, d := range deps {
			if d.path != "" {
				if _, ok := manifests[d.path]; ok {
					edges = append(edges, d.path)
				}
			}
			for _, target := range cw.patched[d.crate] {
				if _, ok := manifests[target]; ok {
					edges = append(edges, target)
				}
			}
		}
		out = append(out, domain.Workspace{Path: dir, Name: *m.Package.Name, Ecosystem: EcosystemRust, Deps: uniqueSorted(edges)})
	}
	return out, nil
}

func newCargoWorkspace(root cargoManifest) (*cargoWorkspace, error) {
	cw := &cargoWorkspace{inherited: root.Workspace.Dependencies, patched: map[string][]string{}}
	if cw.inherited == nil {
		// Non-nil: a member may inherit, and inheriting what the root does
		// not declare is an error, not a skipped dependency.
		cw.inherited = map[string]any{}
	}
	for i, raw := range root.Workspace.Members {
		p, err := normalizePattern(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: workspace member %d (%q): %w", cargoToml, i+1, raw, err)
		}
		cw.include = append(cw.include, p)
		if !strings.ContainsAny(p, "*?") {
			cw.literal = append(cw.literal, p)
		}
	}
	for i, raw := range root.Workspace.Exclude {
		p, err := normalizePattern(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: workspace exclude %d (%q): %w", cargoToml, i+1, raw, err)
		}
		cw.exclude = append(cw.exclude, p)
	}
	for _, source := range sortedKeys(root.Patch) {
		if err := cw.addPatches("[patch."+source+"]", root.Patch[source]); err != nil {
			return nil, err
		}
	}
	if err := cw.addPatches("[replace]", root.Replace); err != nil {
		return nil, err
	}
	return cw, nil
}

// addPatches records the crates a root [patch.<source>] or [replace] table
// points at a directory inside the repository. A [replace] key is
// "name:version" (or "name"); a [patch] key is the crate name.
func (cw *cargoWorkspace) addPatches(where string, table map[string]any) error {
	for _, key := range sortedKeys(table) {
		d, err := cargoDependency(cargoToml+" "+where, ".", key, table[key], nil)
		if err != nil {
			return err
		}
		if d.path == "" {
			continue
		}
		name, _, _ := strings.Cut(d.crate, ":")
		cw.patched[name] = append(cw.patched[name], d.path)
	}
	return nil
}

// memberDirs returns the directories the members globs match that hold a
// Cargo.toml and are not excluded. A member named without a glob must hold
// one: Cargo refuses the workspace otherwise, and dropping it would leave its
// files unattributed without saying so.
func (cw *cargoWorkspace) memberDirs(src Source, files fileSet) ([]string, error) {
	for _, lit := range cw.literal {
		if !files[joinPath(lit, cargoToml)] {
			return nil, fmt.Errorf("%s: workspace member %q, but the tree has no %s", cargoToml, lit, joinPath(lit, cargoToml))
		}
	}
	var dirs []string
	for _, f := range src.Files() {
		if path.Base(f) != cargoToml {
			continue
		}
		dir := path.Dir(f)
		if dir == "." || cw.excluded(dir) {
			continue
		}
		if matchesAny(cw.include, dir) {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// excluded reports whether Cargo leaves the crate at dir out of the
// workspace: it lies at or below an exclude entry and not at or below a
// member named without a glob.
func (cw *cargoWorkspace) excluded(dir string) bool {
	under := func(list []string) bool {
		for _, p := range list {
			if dir == p || strings.HasPrefix(dir, p+"/") || p == "." {
				return true
			}
		}
		return false
	}
	return under(cw.exclude) && !under(cw.literal)
}

// implicitMembers returns the crates m (the member at dir) reaches by a path
// dependency that are not yet members: inside the repository and not
// excluded. Such a crate must have a Cargo.toml in the tree, as Cargo
// requires.
func (cw *cargoWorkspace) implicitMembers(dir string, m cargoManifest, files fileSet, known map[string]cargoManifest, pending []string) ([]string, error) {
	deps, err := cw.deps(dir, m)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range deps {
		if d.path == "" || cw.excluded(d.path) {
			continue
		}
		if _, ok := known[d.path]; ok || slices.Contains(pending, d.path) || slices.Contains(out, d.path) {
			continue
		}
		if err := domain.ValidateWorkspacePath(d.path); err != nil {
			return nil, fmt.Errorf("%s: dependency %q: %w", joinPath(dir, cargoToml), d.key, err)
		}
		if !files[joinPath(d.path, cargoToml)] {
			return nil, fmt.Errorf("%s: dependency %q has path %q, but the tree has no %s", joinPath(dir, cargoToml), d.key, d.raw, joinPath(d.path, cargoToml))
		}
		out = append(out, d.path)
	}
	return out, nil
}

// deps returns every dependency entry of the member at dir.
func (cw *cargoWorkspace) deps(dir string, m cargoManifest) ([]cargoDep, error) {
	file := joinPath(dir, cargoToml)
	var out []cargoDep
	for _, table := range m.depTables() {
		for _, key := range sortedKeys(table) {
			d, err := cargoDependency(file, dir, key, table[key], cw.inherited)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// cargoDependency reads one dependency entry: a version string, or a table
// with an optional `path`, `package` and `workspace = true`. An inherited
// entry is read from inherited (the root's [workspace.dependencies]), with
// its path resolved against the root; inherited is nil where inheritance is
// not allowed.
func cargoDependency(file, dir, key string, value any, inherited map[string]any) (cargoDep, error) {
	d := cargoDep{key: key, crate: key}
	switch v := value.(type) {
	case string:
		return d, nil
	case map[string]any:
		if pkg, ok := v["package"]; ok {
			s, ok := pkg.(string)
			if !ok {
				return d, fmt.Errorf("%s: dependency %q: \"package\" must be a string", file, key)
			}
			d.crate = s
		}
		if w, ok := v["workspace"]; ok && inherited != nil {
			b, ok := w.(bool)
			if !ok {
				return d, fmt.Errorf("%s: dependency %q: \"workspace\" must be true or false", file, key)
			}
			if b {
				entry, ok := inherited[key]
				if !ok {
					return d, fmt.Errorf("%s: dependency %q inherits from [workspace.dependencies], which has no %q", file, key, key)
				}
				return cargoDependency(cargoToml+" [workspace.dependencies]", ".", key, entry, nil)
			}
		}
		if p, ok := v["path"]; ok {
			s, ok := p.(string)
			if !ok {
				return d, fmt.Errorf("%s: dependency %q: \"path\" must be a string", file, key)
			}
			d.raw = s
			d.path, _ = repoDir(dir, s)
		}
		return d, nil
	default:
		return d, fmt.Errorf("%s: dependency %q must be a version string or a table", file, key)
	}
}
