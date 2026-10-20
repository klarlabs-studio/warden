package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Monorepo mode, golden-fleet style: repositories built to a known workspace
// shape, so the right affected selection is computed by hand from the fixture
// rather than from what warden returns, and `warden workspaces --affected
// --json` must agree with it.

// affectedOut is the part of `warden workspaces --affected --json` the
// scenarios assert, decoded independently of warden's own types.
type affectedOut struct {
	Selection struct {
		Kind string `json:"kind"`
		Full *struct {
			Rule string `json:"rule"`
			Path string `json:"path"`
		} `json:"full"`
		Workspaces []struct {
			ID      string `json:"id"`
			Reasons []struct {
				Kind  string   `json:"kind"`
				Paths []string `json:"paths"`
				Via   []string `json:"via"`
			} `json:"reasons"`
		} `json:"workspaces"`
		Removed []string `json:"removed"`
	} `json:"selection"`
}

// selected maps each selected workspace to its reasons, rendered compactly:
// "changed:<paths>" or "dependent:<via>".
func (a affectedOut) selected() map[string][]string {
	out := map[string][]string{}
	for _, w := range a.Selection.Workspaces {
		reasons := []string{}
		for _, r := range w.Reasons {
			switch r.Kind {
			case "changed":
				reasons = append(reasons, "changed:"+join(r.Paths))
			case "dependent":
				reasons = append(reasons, "dependent:"+join(r.Via))
			default:
				reasons = append(reasons, r.Kind)
			}
		}
		out[w.ID] = reasons
	}
	return out
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

func (g *goldenRepo) writeFiles(files map[string]string) {
	g.t.Helper()
	for rel, content := range files {
		p := filepath.Join(g.dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			g.t.Fatal(err)
		}
		g.git("add", "--", rel)
	}
}

// affected runs `warden workspaces --affected --base main --json`, reading
// stdout alone so a diagnostic cannot corrupt the document.
func (g *goldenRepo) affected() affectedOut {
	g.t.Helper()
	cmd := exec.Command(wardenBin, "workspaces", "--affected", "--base", "main", "--json")
	cmd.Dir = g.dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+g.t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		g.t.Fatalf("warden workspaces --affected: %v: %s", err, stderr.String())
	}
	var out affectedOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		g.t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	// Read-only: nothing was noted.
	if notes, _ := exec.Command("git", "-C", g.dir, "notes", "--ref=warden", "list").Output(); len(notes) != 0 {
		g.t.Fatalf("workspaces wrote notes: %s", notes)
	}
	return out
}

// pnpm: utils <- ui <- web, plus api and docs on their own. A file moved from
// api into utils is a delete in api and an add in utils: both are changed, and
// utils' dependents (ui directly, web through ui) follow. docs is untouched.
func TestGoldenFleet_WorkspacesAffectedPNPM(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	g.writeFiles(map[string]string{
		"package.json":                `{"name":"root","private":true}`,
		"pnpm-workspace.yaml":         "packages:\n  - \"packages/*\"\n  - \"apps/*\"\n  - \"!apps/legacy\"\n",
		"packages/utils/package.json": `{"name":"@fx/utils"}`,
		"packages/ui/package.json":    `{"name":"@fx/ui","dependencies":{"@fx/utils":"workspace:*"}}`,
		"apps/web/package.json":       `{"name":"web","devDependencies":{"@fx/ui":"^1.0.0"}}`,
		"apps/api/package.json":       `{"name":"api"}`,
		"apps/api/server.ts":          "export const handler = () => 'a long enough body for rename detection';\n",
		"apps/docs/package.json":      `{"name":"docs"}`,
		"apps/legacy/package.json":    `{"name":"legacy","dependencies":{"@fx/utils":"*"}}`,
	})
	g.commit("fixture")
	g.git("checkout", "-q", "-b", "feat")
	g.git("mv", "apps/api/server.ts", "packages/utils/server.ts")
	g.commit("move server into utils")
	// Untracked on disk, matched by a glob, depending on utils: not committed,
	// so not a workspace.
	if err := os.MkdirAll(filepath.Join(g.dir, "apps", "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.dir, "apps", "scratch", "package.json"), []byte(`{"name":"scratch","dependencies":{"@fx/utils":"*"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := g.affected()
	if got.Selection.Kind != "affected" || got.Selection.Full != nil {
		t.Fatalf("kind %s (%+v), want affected", got.Selection.Kind, got.Selection.Full)
	}
	want := map[string][]string{
		"apps/api":       {"changed:apps/api/server.ts"},
		"apps/web":       {"dependent:packages/ui"},
		"packages/ui":    {"dependent:packages/utils"},
		"packages/utils": {"changed:packages/utils/server.ts"},
	}
	if !reflect.DeepEqual(got.selected(), want) {
		t.Fatalf("selected %v\nwant     %v", got.selected(), want)
	}
}

// go.work with `use .` and nested modules: lib <- svc <- root. A file outside
// every nested module belongs to the root module; a change in lib reaches
// everything that requires it, transitively; a root lockfile forces full.
func TestGoldenFleet_WorkspacesAffectedGoWork(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	g.writeFiles(map[string]string{
		"go.work":        "go 1.26\n\nuse (\n\t.\n\t./svc\n\t./lib\n\t./cli\n)\n",
		"go.mod":         "module example.com/fx\n\ngo 1.26\n\nrequire example.com/fx/svc v0.0.0\n",
		"svc/go.mod":     "module example.com/fx/svc\n\ngo 1.26\n\nrequire example.com/fx/lib v0.0.0\n",
		"svc/svc.go":     "package svc\n",
		"lib/go.mod":     "module example.com/fx/lib\n\ngo 1.26\n",
		"lib/lib.go":     "package lib\n",
		"cli/go.mod":     "module example.com/fx/cli\n\ngo 1.26\n\nreplace example.com/fx/lib => ../lib\n",
		"cli/main.go":    "package main\n",
		"tools/tools.go": "package tools\n",
	})
	g.commit("fixture")
	g.git("checkout", "-q", "-b", "feat")

	g.writeFiles(map[string]string{"tools/tools.go": "package tools // edited\n"})
	g.commit("tools")
	if got, want := g.affected().selected(), map[string][]string{".": {"changed:tools/tools.go"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root-owned change selected %v, want %v", got, want)
	}

	g.writeFiles(map[string]string{"lib/lib.go": "package lib // edited\n"})
	g.commit("lib")
	want := map[string][]string{
		".":   {"changed:tools/tools.go", "dependent:svc"},
		"cli": {"dependent:lib"},
		"lib": {"changed:lib/lib.go"},
		"svc": {"dependent:lib"},
	}
	if got := g.affected().selected(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lib change selected %v\nwant %v", got, want)
	}

	g.writeFiles(map[string]string{"go.work.sum": "example.com/x v1.0.0 h1:abc=\n"})
	g.commit("lockfile")
	got := g.affected()
	if got.Selection.Kind != "full" || got.Selection.Full == nil || got.Selection.Full.Rule != "lockfile" || len(got.Selection.Workspaces) != 4 {
		t.Fatalf("lockfile change: kind %s %+v with %d workspaces, want full by lockfile over all 4", got.Selection.Kind, got.Selection.Full, len(got.Selection.Workspaces))
	}
}

// Cycles, which real monorepos have: two pnpm packages that depend on each
// other (one through devDependencies), web on one of them, and two go.work
// modules that require each other. `warden workspaces` lists the graph rather
// than refusing it, and a change to any member of a cycle selects the whole
// cycle plus its dependents. reports stays out of everything.
func TestGoldenFleet_WorkspacesCycles(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	g.writeFiles(map[string]string{
		"package.json":                  `{"name":"root","private":true}`,
		"pnpm-workspace.yaml":           "packages:\n  - \"packages/*\"\n",
		"packages/a/package.json":       `{"name":"a","dependencies":{"b":"workspace:*"}}`,
		"packages/b/package.json":       `{"name":"b","devDependencies":{"a":"workspace:*"}}`,
		"packages/b/index.ts":           "export const b = 1;\n",
		"packages/web/package.json":     `{"name":"web","dependencies":{"a":"workspace:*"}}`,
		"packages/reports/package.json": `{"name":"reports"}`,
		"go.work":                       "go 1.26\n\nuse (\n\t./mods/x\n\t./mods/y\n)\n",
		"mods/x/go.mod":                 "module example.com/x\n\ngo 1.26\n\nrequire example.com/y v0.0.0\n",
		"mods/y/go.mod":                 "module example.com/y\n\ngo 1.26\n\nrequire example.com/x v0.0.0\n",
		"mods/y/y.go":                   "package y\n",
	})
	g.commit("fixture")

	cmd := exec.Command(wardenBin, "workspaces", "--json")
	cmd.Dir = g.dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("warden workspaces on a cyclic repo: %v: %s", err, stderr.String())
	}
	var list struct {
		Workspaces []struct {
			ID   string   `json:"id"`
			Deps []string `json:"deps"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	gotDeps := map[string][]string{}
	for _, w := range list.Workspaces {
		gotDeps[w.ID] = w.Deps
	}
	wantDeps := map[string][]string{
		"mods/x": {"mods/y"}, "mods/y": {"mods/x"},
		"packages/a": {"packages/b"}, "packages/b": {"packages/a"},
		"packages/reports": {}, "packages/web": {"packages/a"},
	}
	if !reflect.DeepEqual(gotDeps, wantDeps) {
		t.Fatalf("graph %v\nwant  %v", gotDeps, wantDeps)
	}

	g.git("checkout", "-q", "-b", "feat")
	g.writeFiles(map[string]string{"packages/b/index.ts": "export const b = 2;\n"})
	g.commit("b")
	want := map[string][]string{
		"packages/a":   {"dependent:packages/b"},
		"packages/b":   {"changed:packages/b/index.ts", "dependent:packages/a"},
		"packages/web": {"dependent:packages/a"},
	}
	got := g.affected()
	if got.Selection.Kind != "affected" || !reflect.DeepEqual(got.selected(), want) {
		t.Fatalf("JS cycle: kind %s, selected %v\nwant %v", got.Selection.Kind, got.selected(), want)
	}

	g.writeFiles(map[string]string{"mods/y/y.go": "package y // edited\n"})
	g.commit("y")
	want["mods/x"] = []string{"dependent:mods/y"}
	want["mods/y"] = []string{"changed:mods/y/y.go", "dependent:mods/x"}
	got = g.affected()
	if got.Selection.Kind != "affected" || !reflect.DeepEqual(got.selected(), want) {
		t.Fatalf("Go cycle: kind %s, selected %v\nwant %v", got.Selection.Kind, got.selected(), want)
	}
}
