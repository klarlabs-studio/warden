package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"gopkg.in/yaml.v3"
)

// EcosystemJS labels workspaces found through npm, yarn or pnpm workspaces.
const EcosystemJS = "js"

const (
	packageJSON   = "package.json"
	pnpmWorkspace = "pnpm-workspace.yaml"
)

// jsDependencyFields are the package.json fields whose keys name packages a
// workspace depends on. Every kind counts: a devDependency on a sibling still
// means the sibling's change can break this workspace's checks.
var jsDependencyFields = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

// discoverJS returns the workspaces a JS package manager would link: the
// directories matched by pnpm-workspace.yaml's `packages`, or, without that
// file, by the root package.json's `workspaces` (npm and yarn; an array or
// yarn's {"packages": [...]} object). pnpm ignores package.json `workspaces`
// when pnpm-workspace.yaml exists, and so does this.
//
// A matched directory is a workspace when it holds a package.json. Its edges
// are the dependencies, of any kind and any version spec, that name another
// workspace's package.
func discoverJS(src Source, files fileSet) ([]domain.Workspace, error) {
	patterns, origin, err := jsPatterns(src, files)
	if err != nil || origin == "" {
		return nil, err
	}
	include, exclude, err := splitPatterns(origin, patterns)
	if err != nil {
		return nil, err
	}
	var dirs, manifests []string
	for _, f := range src.Files() {
		if path.Base(f) != packageJSON {
			continue
		}
		dir := path.Dir(f)
		if !isJSWorkspaceDir(dir, include, exclude) {
			continue
		}
		dirs = append(dirs, dir)
		manifests = append(manifests, f)
	}
	data, err := readManifests(src, manifests)
	if err != nil {
		return nil, err
	}
	pkgs := make([]jsPackage, 0, len(dirs))
	for _, dir := range dirs {
		p, err := parsePackage(joinPath(dir, packageJSON), data[joinPath(dir, packageJSON)])
		if err != nil {
			return nil, err
		}
		p.dir = dir
		pkgs = append(pkgs, p)
	}
	return jsWorkspaces(pkgs), nil
}

// jsPatterns returns the workspace globs and the file that declared them, or
// an empty origin when the tree declares no JS workspaces.
func jsPatterns(src Source, files fileSet) (patterns []string, origin string, err error) {
	switch {
	case files[pnpmWorkspace]:
		data, err := readManifests(src, []string{pnpmWorkspace})
		if err != nil {
			return nil, "", err
		}
		var doc struct {
			Packages []string `yaml:"packages"`
		}
		if err := yaml.Unmarshal(data[pnpmWorkspace], &doc); err != nil {
			return nil, "", fmt.Errorf("%s: %w", pnpmWorkspace, err)
		}
		return doc.Packages, pnpmWorkspace, nil
	case files[packageJSON]:
		data, err := readManifests(src, []string{packageJSON})
		if err != nil {
			return nil, "", err
		}
		var doc struct {
			Workspaces json.RawMessage `json:"workspaces"`
		}
		if err := json.Unmarshal(trimBOM(data[packageJSON]), &doc); err != nil {
			return nil, "", fmt.Errorf("%s: %w", packageJSON, err)
		}
		patterns, declared, err := workspacesField(doc.Workspaces)
		if err != nil || !declared {
			return nil, "", err
		}
		return patterns, packageJSON, nil
	}
	return nil, "", nil
}

// workspacesField decodes package.json `workspaces`: an array of globs (npm,
// yarn) or an object whose `packages` holds them (yarn classic). declared is
// false when the field is absent or null.
func workspacesField(raw json.RawMessage) (patterns []string, declared bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	if err := json.Unmarshal(raw, &patterns); err == nil {
		return patterns, true, nil
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, fmt.Errorf("%s: \"workspaces\" must be an array of globs or an object with a \"packages\" array", packageJSON)
	}
	return obj.Packages, true, nil
}

// splitPatterns validates and normalizes workspace globs, separating the
// negations ("!glob"). A directory is a workspace when it matches an include
// and no exclude, whatever their order — pnpm's reading, and npm's for every
// list that does not re-include after a negation.
func splitPatterns(origin string, patterns []string) (include, exclude []string, err error) {
	for i, raw := range patterns {
		p, negated := strings.CutPrefix(raw, "!")
		p, err := normalizePattern(p)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: workspace pattern %d (%q): %w", origin, i+1, raw, err)
		}
		if negated {
			exclude = append(exclude, p)
		} else {
			include = append(include, p)
		}
	}
	return include, exclude, nil
}

// normalizePattern turns a package-manager glob into a domain.GlobMatch
// pattern over directories. It refuses what GlobMatch would read differently
// from the package manager (brace and bracket expressions, extglobs) rather
// than let such a pattern match a different set of directories in silence.
func normalizePattern(p string) (string, error) {
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	switch {
	case p == "":
		return "", fmt.Errorf("empty pattern")
	case strings.HasPrefix(p, "/"):
		return "", fmt.Errorf("pattern must be relative to the repository root")
	case strings.ContainsAny(p, "{}[]()\\"):
		return "", fmt.Errorf("brace, bracket, extglob and backslash syntax is not supported; list the directories with '*' and '**' globs")
	}
	for _, elem := range strings.Split(p, "/") {
		if elem == ".." {
			return "", fmt.Errorf("pattern leaves the repository")
		}
	}
	if c := path.Clean(p); c != p {
		return "", fmt.Errorf("pattern is not clean (want %q)", c)
	}
	return p, nil
}

// isJSWorkspaceDir reports whether the directory of a package.json is a
// workspace: matched by an include and by no exclude, and not inside an
// installed dependency tree. The root package is the workspace root, not one
// of its workspaces, unless a pattern names "." itself: a glob such as "*"
// reads "." as a one-element path and would otherwise claim it.
func isJSWorkspaceDir(dir string, include, exclude []string) bool {
	if dir == "." {
		return slices.Contains(include, ".") && !slices.Contains(exclude, ".")
	}
	return !inNodeModules(dir) && matchesAny(include, dir) && !matchesAny(exclude, dir)
}

func matchesAny(globs []string, dir string) bool {
	for _, g := range globs {
		if domain.GlobMatch(g, dir) {
			return true
		}
	}
	return false
}

// inNodeModules reports whether dir lies in an installed dependency tree,
// which no package manager treats as a workspace.
func inNodeModules(dir string) bool {
	for _, elem := range strings.Split(dir, "/") {
		if elem == "node_modules" {
			return true
		}
	}
	return false
}

// jsPackage is the part of a workspace's package.json discovery reads.
type jsPackage struct {
	dir  string
	name string
	// deps are the package names its dependency fields refer to.
	deps []string
}

// parsePackage reads a workspace's name and the package names it depends on.
func parsePackage(file string, data []byte) (jsPackage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(trimBOM(data), &doc); err != nil {
		return jsPackage{}, fmt.Errorf("%s: %w", file, err)
	}
	var p jsPackage
	if raw, ok := doc["name"]; ok {
		if err := json.Unmarshal(raw, &p.name); err != nil {
			return jsPackage{}, fmt.Errorf("%s: \"name\" must be a string", file)
		}
	}
	if p.name == "" {
		return jsPackage{}, fmt.Errorf("%s: a workspace package needs a \"name\"", file)
	}
	for _, field := range jsDependencyFields {
		raw, ok := doc[field]
		if !ok || string(raw) == "null" {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(raw, &deps); err != nil {
			return jsPackage{}, fmt.Errorf("%s: %q must map package names to version strings", file, field)
		}
		for name, spec := range deps {
			p.deps = append(p.deps, name)
			if alias := workspaceAlias(spec); alias != "" {
				p.deps = append(p.deps, alias)
			}
		}
	}
	return p, nil
}

// workspaceAlias returns the package a pnpm `workspace:` alias names
// ("workspace:@scope/real@^1" → "@scope/real"), or "" when spec is not one.
// A plain range after the protocol ("workspace:*", "workspace:^1.2") names
// the dependency's own key, which the caller already has.
func workspaceAlias(spec string) string {
	rest, ok := strings.CutPrefix(spec, "workspace:")
	if !ok || rest == "" {
		return ""
	}
	switch rest[0] {
	case '*', '^', '~', '=', '<', '>', 'v', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return ""
	}
	if at := strings.LastIndex(rest, "@"); at > 0 {
		return rest[:at]
	}
	return rest
}

// jsWorkspaces turns parsed packages into workspaces, with an edge wherever a
// package depends on another workspace's name.
func jsWorkspaces(pkgs []jsPackage) []domain.Workspace {
	byName := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		byName[p.name] = p.dir
	}
	out := make([]domain.Workspace, 0, len(pkgs))
	for _, p := range pkgs {
		deps := []string{}
		for _, name := range p.deps {
			if dir, ok := byName[name]; ok {
				deps = append(deps, dir)
			}
		}
		sort.Strings(deps)
		out = append(out, domain.Workspace{Path: p.dir, Name: p.name, Ecosystem: EcosystemJS, Deps: deps})
	}
	return out
}

// trimBOM drops a UTF-8 byte-order mark, which npm tolerates at the start of
// package.json and encoding/json does not.
func trimBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
}
