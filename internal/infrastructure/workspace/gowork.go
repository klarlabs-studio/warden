package workspace

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"golang.org/x/mod/modfile"
)

// EcosystemGo labels the modules of a go.work workspace.
const EcosystemGo = "go"

const (
	goWork = "go.work"
	goMod  = "go.mod"
)

// discoverGo returns the modules a root go.work uses. A repository with a
// single go.mod and no go.work is not a monorepo here: its packages would
// need `go list` to relate, and discovery never runs the go command.
//
// A module depends on another workspace module when its go.mod requires that
// module's path, or when a replace directive (in go.work or in its go.mod)
// points a module path it requires, or any module path at all, at the other
// module's directory. Every such reading adds an edge; none removes one, so a
// replace cannot hide a require.
func discoverGo(src Source, files fileSet) ([]domain.Workspace, error) {
	if !files[goWork] {
		return nil, nil
	}
	data, err := readManifests(src, []string{goWork})
	if err != nil {
		return nil, err
	}
	work, err := modfile.ParseWork(goWork, data[goWork], nil)
	if err != nil {
		return nil, err
	}
	dirs, err := useDirs(work, files)
	if err != nil {
		return nil, err
	}
	modFiles := make([]string, len(dirs))
	for i, dir := range dirs {
		modFiles[i] = joinPath(dir, goMod)
	}
	mods, err := readManifests(src, modFiles)
	if err != nil {
		return nil, err
	}
	parsed := make(map[string]*modfile.File, len(dirs))
	byModule := map[string][]string{} // module path -> workspace dirs
	for _, dir := range dirs {
		file := joinPath(dir, goMod)
		f, err := modfile.Parse(file, mods[file], nil)
		if err != nil {
			return nil, err
		}
		if f.Module == nil || f.Module.Mod.Path == "" {
			return nil, fmt.Errorf("%s: no module directive", file)
		}
		parsed[dir] = f
		byModule[f.Module.Mod.Path] = append(byModule[f.Module.Mod.Path], dir)
	}
	isWorkspace := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		isWorkspace[d] = true
	}
	workReplace := localReplacements(".", work.Replace, isWorkspace)

	out := make([]domain.Workspace, 0, len(dirs))
	for _, dir := range dirs {
		f := parsed[dir]
		modReplace := localReplacements(dir, f.Replace, isWorkspace)
		var deps []string
		for _, req := range f.Require {
			deps = append(deps, byModule[req.Mod.Path]...)
			deps = append(deps, workReplace[req.Mod.Path]...)
			deps = append(deps, modReplace[req.Mod.Path]...)
		}
		for _, targets := range modReplace {
			deps = append(deps, targets...)
		}
		out = append(out, domain.Workspace{Path: dir, Name: f.Module.Mod.Path, Ecosystem: EcosystemGo, Deps: uniqueSorted(deps)})
	}
	return out, nil
}

// useDirs returns the workspace directory of every go.work `use` directive,
// in repository path space. A directive that leaves the repository, or names
// a directory whose go.mod the tree does not hold, is an error: dropping it
// would leave that module's files unattributed without saying so.
func useDirs(work *modfile.WorkFile, files fileSet) ([]string, error) {
	seen := map[string]bool{}
	var dirs []string
	for _, u := range work.Use {
		dir, ok := repoDir(".", u.Path)
		if !ok {
			return nil, fmt.Errorf("%s: use %q is outside the repository", goWork, u.Path)
		}
		if err := domain.ValidateWorkspacePath(dir); err != nil {
			return nil, fmt.Errorf("%s: use %q: %w", goWork, u.Path, err)
		}
		if !files[joinPath(dir, goMod)] {
			return nil, fmt.Errorf("%s: use %q, but the tree has no %s", goWork, u.Path, joinPath(dir, goMod))
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// localReplacements maps each module path replaced by a directory to that
// directory, when the directory (resolved against from, the directory of the
// file declaring the replace) is a workspace. A replace to a version, or to a
// directory that is not a workspace, links no workspaces.
func localReplacements(from string, replaces []*modfile.Replace, isWorkspace map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, r := range replaces {
		if r.New.Version != "" {
			continue
		}
		if dir, ok := repoDir(from, r.New.Path); ok && isWorkspace[dir] {
			out[r.Old.Path] = append(out[r.Old.Path], dir)
		}
	}
	return out
}

// repoDir resolves a directory named in a Go manifest against from, in
// repository path space. Backslashes are read as separators, as the go
// command does on Windows. It reports false for an absolute path or one that
// leaves the repository.
func repoDir(from, p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" || path.IsAbs(p) || (len(p) > 1 && p[1] == ':') {
		return "", false
	}
	dir := path.Clean(path.Join(from, p))
	if dir == ".." || strings.HasPrefix(dir, "../") {
		return "", false
	}
	return dir, true
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
