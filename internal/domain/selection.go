package domain

import (
	"fmt"
	"path"
	"strings"
)

// SelectionVersion is the version of the affected-selection algorithm. It is
// part of the selection digest: a record names the rules it was selected by, so
// changing what Select returns for some input must bump it.
const SelectionVersion = 1

// WardenConfigFile is the repository configuration file. A change to it can
// change what every step runs, so it forces a full selection. (The config
// loader in infrastructure reads this constant.)
const WardenConfigFile = ".warden.yaml"

// WorkspaceRootManifests are the manifests at the repository root. A change
// can add, remove or rewire workspaces in ways the per-path rules cannot see,
// so it forces a full selection. The list is matched against root paths
// only, before ownership: when the root is itself a workspace ("." in
// go.work, or a root package), its go.mod and package.json still force full
// rather than selecting just the root workspace. That is the conservative
// reading: a root go.mod's replace and toolchain directives, like a root
// package.json's overrides and workspaces, can change every workspace.
var WorkspaceRootManifests = []string{"package.json", "pnpm-workspace.yaml", "go.work", "go.mod"}

// RootLockfiles are the shared lockfiles at the repository root. One resolves
// dependencies for every workspace and nothing attributes a change in it to
// particular workspaces yet, so a change forces a full selection. A root
// module's go.sum is included for the same reason as go.mod in
// WorkspaceRootManifests. A lockfile inside a workspace below the root belongs
// to that workspace like any other file there.
var RootLockfiles = []string{
	"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock",
	"bun.lock", "bun.lockb", "go.work.sum", "go.sum",
}

// DefaultGlobalPaths are the globs that affect every workspace in the
// ecosystems warden discovers: shared package-manager and build-tool
// configuration. They apply in addition to SelectionRules.Global, which can
// add to them but never remove one.
var DefaultGlobalPaths = []string{
	".npmrc", ".yarnrc.yml", ".yarn/**", ".pnpmfile.cjs",
	"tsconfig.base.json", "turbo.json", "nx.json",
}

// SelectionKind says whether every workspace was selected or only the
// affected ones.
type SelectionKind string

const (
	SelectionFull     SelectionKind = "full"
	SelectionAffected SelectionKind = "affected"
)

// UnownedMode says what a changed path that belongs to no workspace does.
type UnownedMode string

const (
	// UnownedFull forces a full selection. It is the default (also for ""):
	// a path nobody owns may be read by anything.
	UnownedFull UnownedMode = "full"
	// UnownedIgnore lets such paths select nothing.
	UnownedIgnore UnownedMode = "ignore"
)

// SelectionRules are the configured inputs to Select beyond the graph and the
// changed paths. They decide what a change means, so they are part of the
// selection digest.
type SelectionRules struct {
	// Global globs force a full selection when any changed path matches.
	// They add to DefaultGlobalPaths.
	Global []string `json:"global"`
	// Ignore globs exempt changed paths that belong to no workspace from
	// UnownedFull. They never exempt a workspace's own files, the config
	// file, a root manifest, a root lockfile or a global path.
	Ignore []string `json:"ignore"`
	// Unowned is "full" (also the zero value) or "ignore".
	Unowned UnownedMode `json:"unowned"`
}

// Validate refuses rules Select cannot apply faithfully.
func (r SelectionRules) Validate() error {
	switch r.Unowned {
	case "", UnownedFull, UnownedIgnore:
	default:
		return fmt.Errorf("workspaces.unowned must be %q or %q, not %q", UnownedFull, UnownedIgnore, r.Unowned)
	}
	for _, list := range []struct {
		key   string
		globs []string
	}{{"workspaces.global", r.Global}, {"workspaces.ignore", r.Ignore}} {
		for _, g := range list.globs {
			if err := validateSelectionGlob(g); err != nil {
				return fmt.Errorf("%s: %w", list.key, err)
			}
		}
	}
	return nil
}

func validateSelectionGlob(g string) error {
	switch {
	case g == "":
		return fmt.Errorf("empty glob")
	case len(g) > maxGlobLen:
		return fmt.Errorf("glob longer than %d bytes", maxGlobLen)
	case strings.HasPrefix(g, "/"):
		return fmt.Errorf("glob %q must be relative to the repository root", g)
	case strings.ContainsRune(g, '\\'):
		// Changed paths always use "/", so a backslash can never match one.
		return fmt.Errorf("glob %q must separate directories with /", g)
	case strings.Count(g, "/") >= maxGlobSegments:
		return fmt.Errorf("glob %q has more than %d segments", g, maxGlobSegments)
	}
	for _, seg := range strings.Split(g, "/") {
		// A changed path is clean, so these segments can never match: the
		// glob would look like configuration and select nothing.
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("glob %q has an empty, \".\" or \"..\" segment, which no repository path contains", g)
		}
	}
	for _, r := range g {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("glob %q contains a control character", g)
		}
	}
	return nil
}

// canonical returns the rules with sorted, de-duplicated globs and the default
// unowned mode spelled out, so equal rules digest equally.
func (r SelectionRules) canonical() SelectionRules {
	out := SelectionRules{Global: sortedUnique(r.Global), Ignore: sortedUnique(r.Ignore), Unowned: r.Unowned}
	if out.Unowned == "" {
		out.Unowned = UnownedFull
	}
	return out
}

// SelectionInput is everything Select reads.
type SelectionInput struct {
	// Base is the graph at the anchor's tree, Head the graph at the tree
	// being checked. Selection runs over their union, so a workspace (or an
	// edge) that the change deletes still reaches its former dependents.
	Base, Head WorkspaceGraph
	// Changed are the paths that differ between the two trees (from
	// `git diff --name-status -z --no-renames`, so a rename is a delete plus
	// an add and both sides are attributed).
	Changed []string
	Rules   SelectionRules
	// AnchorConfigurationDigest and ConfigurationDigest are the effective
	// configuration digests at the anchor and now. Unless both are known
	// and equal the selection is full: a change to an `extends:` base never
	// shows up as a changed path.
	AnchorConfigurationDigest string
	ConfigurationDigest       string
}

// FullRule names the rule that forced a full selection.
type FullRule string

// The rules, in the order Select applies them. The first that holds wins.
const (
	FullRuleConfiguration FullRule = "configuration" // the effective configuration changed or is unknown
	FullRuleInvalidPath   FullRule = "invalid-path"  // a changed path is not a clean repository path
	FullRuleRootManifest  FullRule = "root-manifest" // a workspace-root manifest changed
	FullRuleLockfile      FullRule = "lockfile"      // a root lockfile changed
	FullRuleGlobal        FullRule = "global"        // a changed path matches a global glob
	FullRuleUnowned       FullRule = "unowned"       // a changed path belongs to no workspace and is not ignored
)

// ForcedFull explains why a selection is full.
type ForcedFull struct {
	Rule FullRule `json:"rule"`
	// Path is the first changed path (in sorted order) the rule held for;
	// empty for FullRuleConfiguration when no path is involved.
	Path string `json:"path,omitempty"`
	// Pattern is the glob that matched, for FullRuleGlobal.
	Pattern string `json:"pattern,omitempty"`
}

// ReasonKind says why one workspace was selected.
type ReasonKind string

const (
	// ReasonChanged: a changed path lies in the workspace (Paths lists them).
	ReasonChanged ReasonKind = "changed"
	// ReasonDependent: the workspace depends on affected workspaces (Via
	// lists them; some may be workspaces the change removed).
	ReasonDependent ReasonKind = "dependent"
	// ReasonFull: the whole selection was forced full (see Selection.Full).
	ReasonFull ReasonKind = "full"
)

// SelectionReason is one reason a workspace was selected.
type SelectionReason struct {
	Kind  ReasonKind `json:"kind"`
	Paths []string   `json:"paths,omitempty"`
	Via   []string   `json:"via,omitempty"`
}

// SelectedWorkspace is a workspace of the head graph that must be checked,
// with every reason it was selected.
type SelectedWorkspace struct {
	Path    string            `json:"id"`
	Name    string            `json:"name"`
	Reasons []SelectionReason `json:"reasons"`
}

// Selection is the result of Select, together with the canonical inputs it
// was computed from, so that it can explain itself and be digested.
type Selection struct {
	Version int           `json:"version"`
	Kind    SelectionKind `json:"kind"`
	// Full is set exactly when Kind is SelectionFull.
	Full *ForcedFull `json:"full,omitempty"`
	// Workspaces are the selected head workspaces, sorted by path.
	Workspaces []SelectedWorkspace `json:"workspaces"`
	// Removed are affected workspaces that exist only in the base graph:
	// nothing runs for them, but their former dependents were selected.
	Removed []string `json:"removed"`

	BaseGraphDigest           string         `json:"base_graph_digest"`
	HeadGraphDigest           string         `json:"head_graph_digest"`
	Changed                   []string       `json:"changed"`
	Rules                     SelectionRules `json:"rules"`
	AnchorConfigurationDigest string         `json:"anchor_configuration_digest"`
	ConfigurationDigest       string         `json:"configuration_digest"`
}

// Paths returns the selected workspace paths, sorted.
func (s Selection) Paths() []string {
	out := make([]string, 0, len(s.Workspaces))
	for _, w := range s.Workspaces {
		out = append(out, w.Path)
	}
	return out
}

// Digest returns the digest of the selection's canonical encoding: the
// algorithm version, both graph digests, the changed paths, the rules, both
// configuration digests and the result with its reasons. Select produces the
// canonical form, so the digest does not depend on input order.
func (s Selection) Digest() (string, error) {
	return DigestJSON(s)
}

// Select maps the paths a change touches to the workspaces that must be
// checked. It is pure and conservative: whenever it cannot attribute a change
// to particular workspaces it selects all of them.
//
// Rules, in order; the first that forces full wins:
//
//  1. the configuration digests differ or either is unknown, or
//     WardenConfigFile changed;
//  2. a changed path is not a clean, relative repository path;
//  3. a WorkspaceRootManifests file changed at the root;
//  4. a RootLockfiles file changed at the root;
//  5. a changed path matches DefaultGlobalPaths or Rules.Global;
//  6. a changed path belongs to no workspace of either graph, matches no
//     Rules.Ignore glob, and Rules.Unowned is not "ignore". (With a root
//     workspace "." every path is owned, so this rule never fires; rules
//     1-5 still apply, because they are checked before ownership.)
//
// Otherwise each changed path selects the workspace that owns it (the longest
// workspace path that contains it, "." lowest, in the base and in the head
// graph), and
// every workspace that depends on a selected one, transitively, over the
// union of both graphs' edges, is selected too.
//
// A full selection is exactly every workspace of the head graph. Select
// returns an error only for invalid input (a graph or rules that fail
// validation); it never drops what it cannot read.
func Select(in SelectionInput) (Selection, error) {
	if err := in.Rules.Validate(); err != nil {
		return Selection{}, err
	}
	baseDigest, err := in.Base.Digest()
	if err != nil {
		return Selection{}, fmt.Errorf("base workspace graph: %w", err)
	}
	headDigest, err := in.Head.Digest()
	if err != nil {
		return Selection{}, fmt.Errorf("head workspace graph: %w", err)
	}
	sel := Selection{
		Version:                   SelectionVersion,
		BaseGraphDigest:           baseDigest,
		HeadGraphDigest:           headDigest,
		Changed:                   sortedUnique(in.Changed),
		Rules:                     in.Rules.canonical(),
		AnchorConfigurationDigest: in.AnchorConfigurationDigest,
		ConfigurationDigest:       in.ConfigurationDigest,
		Workspaces:                []SelectedWorkspace{},
		Removed:                   []string{},
	}
	head := in.Head.Canonical()
	base := in.Base.Canonical()

	if full := forcedFull(in, sel.Changed, sel.Rules, base, head); full != nil {
		sel.Kind = SelectionFull
		sel.Full = full
		for _, w := range head.Workspaces {
			sel.Workspaces = append(sel.Workspaces, SelectedWorkspace{
				Path: w.Path, Name: w.Name,
				Reasons: []SelectionReason{{Kind: ReasonFull}},
			})
		}
		return sel, nil
	}
	sel.Kind = SelectionAffected
	selectAffected(&sel, base, head)
	return sel, nil
}

// forcedFull applies the rules that force a full selection and returns the
// first that holds, or nil.
func forcedFull(in SelectionInput, changed []string, rules SelectionRules, base, head WorkspaceGraph) *ForcedFull {
	if in.AnchorConfigurationDigest == "" || in.AnchorConfigurationDigest != in.ConfigurationDigest {
		return &ForcedFull{Rule: FullRuleConfiguration}
	}
	if containsString(changed, WardenConfigFile) {
		return &ForcedFull{Rule: FullRuleConfiguration, Path: WardenConfigFile}
	}
	for _, p := range changed {
		if !validChangedPath(p) {
			return &ForcedFull{Rule: FullRuleInvalidPath, Path: p}
		}
	}
	for _, p := range changed {
		if containsString(WorkspaceRootManifests, p) {
			return &ForcedFull{Rule: FullRuleRootManifest, Path: p}
		}
	}
	for _, p := range changed {
		if containsString(RootLockfiles, p) {
			return &ForcedFull{Rule: FullRuleLockfile, Path: p}
		}
	}
	globals := append(append([]string{}, DefaultGlobalPaths...), rules.Global...)
	for _, p := range changed {
		for _, g := range globals {
			if GlobMatch(g, p) {
				return &ForcedFull{Rule: FullRuleGlobal, Path: p, Pattern: g}
			}
		}
	}
	if rules.Unowned == UnownedIgnore {
		return nil
	}
	for _, p := range changed {
		if owner(base, p) == "" && owner(head, p) == "" && !matchesAny(rules.Ignore, p) {
			return &ForcedFull{Rule: FullRuleUnowned, Path: p}
		}
	}
	return nil
}

// selectAffected fills sel with the owners of the changed paths and their
// reverse-transitive dependents over the union of both graphs.
func selectAffected(sel *Selection, base, head WorkspaceGraph) {
	changedBy := map[string][]string{} // workspace path -> owned changed paths
	for _, p := range sel.Changed {
		for _, o := range []string{owner(base, p), owner(head, p)} {
			if o != "" && !containsString(changedBy[o], p) {
				changedBy[o] = append(changedBy[o], p)
			}
		}
	}

	deps := unionDeps(base, head)
	dependents := map[string][]string{}
	for w, ds := range deps {
		for _, d := range ds {
			dependents[d] = append(dependents[d], w)
		}
	}
	affected := map[string]bool{}
	queue := sortedKeys(changedBy)
	for _, w := range queue {
		affected[w] = true
	}
	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		for _, d := range dependents[w] {
			if !affected[d] {
				affected[d] = true
				queue = append(queue, d)
			}
		}
	}

	inHead := map[string]bool{}
	for _, w := range head.Workspaces {
		inHead[w.Path] = true
		if !affected[w.Path] {
			continue
		}
		var reasons []SelectionReason
		if paths := changedBy[w.Path]; len(paths) > 0 {
			reasons = append(reasons, SelectionReason{Kind: ReasonChanged, Paths: sortedUnique(paths)})
		}
		var via []string
		for _, d := range deps[w.Path] {
			if affected[d] && d != w.Path {
				via = append(via, d)
			}
		}
		if len(via) > 0 {
			reasons = append(reasons, SelectionReason{Kind: ReasonDependent, Via: via})
		}
		sel.Workspaces = append(sel.Workspaces, SelectedWorkspace{Path: w.Path, Name: w.Name, Reasons: reasons})
	}
	for _, w := range sortedKeys(affected) {
		if !inHead[w] {
			sel.Removed = append(sel.Removed, w)
		}
	}
}

// unionDeps returns every workspace of either graph with the union of its
// dependencies in both, each list sorted.
func unionDeps(base, head WorkspaceGraph) map[string][]string {
	deps := map[string][]string{}
	for _, g := range []WorkspaceGraph{base, head} {
		for _, w := range g.Workspaces {
			deps[w.Path] = append(deps[w.Path], w.Deps...)
		}
	}
	for w, ds := range deps {
		deps[w] = sortedUnique(ds)
	}
	return deps
}

// owner returns the path of the workspace of g that owns p: the longest
// workspace path that is p or a directory above it, with the root workspace
// "." containing every path at the lowest priority. It returns "" when no
// workspace contains p. A nested workspace therefore owns its own files, and
// its parent owns only the rest (it is selected for the nested one's changes
// only if it declares a dependency on it).
func owner(g WorkspaceGraph, p string) string {
	best, bestRank := "", -1
	for _, w := range g.Workspaces {
		if pathWithin(p, w.Path) && ownerRank(w.Path) > bestRank {
			best, bestRank = w.Path, ownerRank(w.Path)
		}
	}
	return best
}

// ownerRank orders containing workspaces: the root "." lowest (rank 0, even
// though it is as long as a one-letter path), then by path length.
func ownerRank(dir string) int {
	if dir == "." {
		return 0
	}
	return len(dir)
}

// validChangedPath reports whether p is a clean, relative, "/"-separated
// repository path below the root. Anything else cannot be attributed to a
// workspace safely. (A rename rendered as "a/{x => y}/f" or a C-quoted path is
// a valid string here; it matches no workspace and is caught as unowned.)
func validChangedPath(p string) bool {
	if p == "" || len(p) > maxGlobLen || strings.HasPrefix(p, "/") ||
		strings.ContainsRune(p, '\\') || path.Clean(p) != p ||
		p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// pathWithin reports whether p is dir or lies beneath it. Every path lies
// beneath the root, ".".
func pathWithin(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

func matchesAny(globs []string, p string) bool {
	for _, g := range globs {
		if GlobMatch(g, p) {
			return true
		}
	}
	return false
}

func containsString(xs []string, x string) bool {
	return indexOf(xs, x) >= 0
}
