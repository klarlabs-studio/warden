package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// memTree is an in-memory Source: the tree a test describes, nothing else.
type memTree map[string]string

func (m memTree) Files() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (m memTree) Read(paths []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, p := range paths {
		if c, ok := m[p]; ok {
			out[p] = []byte(c)
		}
	}
	return out, nil
}

func ws(path, name, eco string, deps ...string) domain.Workspace {
	if deps == nil {
		deps = []string{}
	}
	return domain.Workspace{Path: path, Name: name, Ecosystem: eco, Deps: deps}
}

func mustDiscover(t *testing.T, src Source) []domain.Workspace {
	t.Helper()
	g, err := Discover(src)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return g.Workspaces
}

func assertWorkspaces(t *testing.T, got, want []domain.Workspace) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaces:\n got  %+v\n want %+v", got, want)
	}
}

func TestDiscover_PNPM(t *testing.T) {
	tree := memTree{
		"package.json": `{"name":"root","workspaces":["ignored/*"]}`,
		"pnpm-workspace.yaml": `packages:
  - "packages/*"
  - "./apps/*/"
  - "!packages/legacy"
catalog:
  react: ^19
`,
		"ignored/x/package.json":       `{"name":"ignored-x"}`,
		"packages/ui/package.json":     `{"name":"@acme/ui","dependencies":{"@acme/utils":"workspace:^","react":"^19"}}`,
		"packages/utils/package.json":  "\xef\xbb\xbf" + `{"name":"@acme/utils","version":"1.0.0"}`,
		"packages/legacy/package.json": `{"name":"@acme/legacy"}`,
		"packages/readme.md":           "not a workspace",
		"packages/empty/src/index.ts":  "no package.json, no workspace",
		"apps/web/package.json": `{"name":"web",
			"dependencies":{"@acme/ui":"workspace:*"},
			"devDependencies":{"utils-alias":"workspace:@acme/utils@*"},
			"peerDependencies":null}`,
		"apps/web/node_modules/@acme/ui/package.json": `{"name":"@acme/ui"}`,
		"apps/docs/package.json":                      `{"name":"docs","optionalDependencies":{"@acme/legacy":"1.0.0","web":"^0.1.0"}}`,
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws("apps/docs", "docs", "js", "apps/web"),
		ws("apps/web", "web", "js", "packages/ui", "packages/utils"),
		ws("packages/ui", "@acme/ui", "js", "packages/utils"),
		ws("packages/utils", "@acme/utils", "js"),
	})
}

func TestDiscover_NPMAndYarnWorkspaces(t *testing.T) {
	for name, root := range map[string]string{
		"npm array":   `{"name":"root","private":true,"workspaces":["packages/**"]}`,
		"yarn object": `{"name":"root","workspaces":{"packages":["packages/**"],"nohoist":["**/x"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			tree := memTree{
				"package.json":                  root,
				"packages/a/package.json":       `{"name":"a","devDependencies":{"b":"1.2.3"}}`,
				"packages/group/b/package.json": `{"name":"b"}`,
				"tools/package.json":            `{"name":"tools"}`,
			}
			assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
				ws("packages/a", "a", "js", "packages/group/b"),
				ws("packages/group/b", "b", "js"),
			})
		})
	}
}

func TestDiscover_NoWorkspacesDeclared(t *testing.T) {
	for name, tree := range map[string]memTree{
		"empty tree":            {},
		"plain package":         {"package.json": `{"name":"app","dependencies":{"x":"1"}}`},
		"null workspaces":       {"package.json": `{"name":"app","workspaces":null}`},
		"pnpm without packages": {"pnpm-workspace.yaml": "onlyBuiltDependencies: [esbuild]\n", "a/package.json": `{"name":"a"}`},
		"single go module":      {"go.mod": "module example.com/one\n", "sub/go.mod": "module example.com/one/sub\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mustDiscover(t, tree); len(got) != 0 {
				t.Fatalf("got %+v, want no workspaces", got)
			}
		})
	}
}

func TestDiscover_GoWork(t *testing.T) {
	tree := memTree{
		"go.work": `go 1.26

use .

use (
	./svc/api
	lib/core
	./lib/core
	./tools/gen
)

replace example.com/vendored => ./lib/core
replace example.com/elsewhere => ../outside
replace example.com/pinned => example.com/fork v1.0.0
`,
		"go.mod": `module example.com/root

go 1.26

require example.com/svc/api v0.0.0
`,
		"svc/api/go.mod": `module example.com/svc/api

go 1.26

require (
	example.com/vendored v1.0.0
	golang.org/x/mod v0.42.0
)
`,
		"lib/core/go.mod": "module example.com/lib/core\n\ngo 1.26\n",
		"tools/gen/go.mod": `module example.com/tools/gen

go 1.26

replace example.com/anything => ../../lib/core
`,
		"lib/notused/go.mod": "module example.com/lib/notused\n",
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws(".", "example.com/root", "go", "svc/api"),
		ws("lib/core", "example.com/lib/core", "go"),
		ws("svc/api", "example.com/svc/api", "go", "lib/core"),
		ws("tools/gen", "example.com/tools/gen", "go", "lib/core"),
	})
}

func TestDiscover_JSAndGoTogether(t *testing.T) {
	tree := memTree{
		"pnpm-workspace.yaml":    "packages: [web]\n",
		"web/package.json":       `{"name":"web"}`,
		"go.work":                "go 1.26\nuse ./api\n",
		"api/go.mod":             "module example.com/api\n",
		"api/internal/x/main.go": "package x",
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws("api", "example.com/api", "go"),
		ws("web", "web", "js"),
	})
}

// Every malformed or unusable manifest is an error that names the file; none
// is skipped, because a skipped workspace is one nobody checks.
func TestDiscover_MalformedManifestsAreErrors(t *testing.T) {
	big := `{"name":"big","description":"` + strings.Repeat("x", maxManifestBytes) + `"}`
	cases := map[string]struct {
		tree memTree
		want string
	}{
		"root package.json is not JSON": {
			memTree{"package.json": `{"workspaces": [`}, "package.json:"},
		"workspaces has the wrong type": {
			memTree{"package.json": `{"workspaces":"packages/*"}`}, `"workspaces" must be an array`},
		"pnpm-workspace.yaml is not YAML": {
			memTree{"pnpm-workspace.yaml": "packages: [a\n"}, "pnpm-workspace.yaml:"},
		"pnpm packages are not strings": {
			memTree{"pnpm-workspace.yaml": "packages:\n  - {a: b}\n"}, "pnpm-workspace.yaml:"},
		"workspace package.json is not JSON": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":`}, "a/package.json:"},
		"workspace has no name": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"version":"1.0.0"}`}, `a/package.json: a workspace package needs a "name"`},
		"name is not a string": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":7}`}, `"name" must be a string`},
		"dependencies are not a map of strings": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":"a","dependencies":["b"]}`}, `"dependencies" must map package names`},
		"brace pattern": {
			memTree{"package.json": `{"workspaces":["packages/{a,b}"]}`}, "brace, bracket"},
		"pattern leaves the repository": {
			memTree{"package.json": `{"workspaces":["../x/*"]}`}, "leaves the repository"},
		"absolute pattern": {
			memTree{"pnpm-workspace.yaml": "packages: [\"/abs\"]\n"}, "relative to the repository root"},
		"empty pattern": {
			memTree{"pnpm-workspace.yaml": "packages: [\"!\"]\n"}, "empty pattern"},
		"unclean pattern": {
			memTree{"pnpm-workspace.yaml": "packages: [\"a//b\"]\n"}, "not clean"},
		"oversized manifest": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": big}, "larger than"},
		"go.work is malformed": {
			memTree{"go.work": "use (\n"}, "go.work:"},
		"go.work uses a directory without go.mod": {
			memTree{"go.work": "go 1.26\nuse ./missing\n"}, "the tree has no missing/go.mod"},
		"go.work uses a directory outside the repository": {
			memTree{"go.work": "go 1.26\nuse ../sibling\n"}, "outside the repository"},
		"go.work uses an absolute directory": {
			memTree{"go.work": "go 1.26\nuse /abs/mod\n"}, "outside the repository"},
		"go.work uses a directory warden cannot name": {
			memTree{"go.work": "go 1.26\nuse ./-flag\n", "-flag/go.mod": "module example.com/flag\n"}, "starting with '-'"},
		"go.mod is malformed": {
			memTree{"go.work": "go 1.26\nuse ./a\n", "a/go.mod": "module example.com/a\nrequire (\n"}, "a/go.mod:"},
		"go.mod has no module directive": {
			memTree{"go.work": "go 1.26\nuse ./a\n", "a/go.mod": "go 1.26\n"}, "a/go.mod: no module directive"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Discover(tc.tree)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Discover error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// Dependency cycles are common in real monorepos and are not an error: a JS
// devDependency cycle and a Go require cycle are discovered as they are.
func TestDiscover_Cycles(t *testing.T) {
	t.Run("JS devDependency cycle and self-dependency", func(t *testing.T) {
		got := mustDiscover(t, memTree{
			"package.json":   `{"workspaces":["*"]}`,
			"a/package.json": `{"name":"a","dependencies":{"b":"workspace:*"}}`,
			"b/package.json": `{"name":"b","devDependencies":{"a":"workspace:*"}}`,
			"c/package.json": `{"name":"c","devDependencies":{"c":"*","a":"^1"}}`,
		})
		assertWorkspaces(t, got, []domain.Workspace{
			ws("a", "a", EcosystemJS, "b"),
			ws("b", "b", EcosystemJS, "a"),
			ws("c", "c", EcosystemJS, "a", "c"),
		})
	})
	t.Run("Go require cycle", func(t *testing.T) {
		got := mustDiscover(t, memTree{
			"go.work":  "go 1.26\nuse (\n\t./x\n\t./y\n\t./z\n)\n",
			"x/go.mod": "module example.com/x\n\ngo 1.26\n\nrequire example.com/y v0.0.0\n",
			"y/go.mod": "module example.com/y\n\ngo 1.26\n\nrequire example.com/z v0.0.0\n",
			"z/go.mod": "module example.com/z\n\ngo 1.26\n\nrequire example.com/x v0.0.0\n",
		})
		assertWorkspaces(t, got, []domain.Workspace{
			ws("x", "example.com/x", EcosystemGo, "y"),
			ws("y", "example.com/y", EcosystemGo, "z"),
			ws("z", "example.com/z", EcosystemGo, "x"),
		})
	})
}

// A manifest whose workspaces fail domain validation fails discovery with the
// validation's reason; nothing is dropped to make the graph pass.
func TestDiscover_InvalidGraphSurfacesTheValidationError(t *testing.T) {
	cases := map[string]struct {
		tree memTree
		want string
	}{
		"name with a shell metacharacter": {
			memTree{"package.json": `{"workspaces":["a","b"]}`, "a/package.json": `{"name":"a; rm -rf ~"}`, "b/package.json": `{"name":"b"}`},
			`workspace "a" has an invalid name "a; rm -rf ~"`},
		"two workspaces under one name": {
			memTree{"package.json": `{"workspaces":["*"]}`, "a/package.json": `{"name":"same"}`, "b/package.json": `{"name":"same"}`},
			`workspace name "same" is used by both`},
		"one directory in two ecosystems": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":"a"}`, "go.work": "go 1.26\nuse ./a\n", "a/go.mod": "module example.com/a\n"},
			`workspace path "a" is declared twice`},
		"go module path outside the name class": {
			memTree{"go.work": "go 1.26\nuse ./a\n", "a/go.mod": "module example.com/a+b\n"},
			"invalid name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, err := Discover(tc.tree)
			if err == nil || !strings.Contains(err.Error(), "discovered workspaces are invalid") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Discover = %+v, %v; want an error containing %q", g, err, tc.want)
			}
		})
	}
}

func TestWorkspaceAlias(t *testing.T) {
	for spec, want := range map[string]string{
		"workspace:*":                  "",
		"workspace:^1.2.0":             "",
		"workspace:~":                  "",
		"workspace:1.0.0":              "",
		"workspace:":                   "",
		"^1.0.0":                       "",
		"npm:other@1":                  "",
		"workspace:real":               "real",
		"workspace:real@*":             "real",
		"workspace:@scope/real@^1.0.0": "@scope/real",
		"workspace:@scope/real":        "@scope/real",
	} {
		if got := workspaceAlias(spec); got != want {
			t.Errorf("workspaceAlias(%q) = %q, want %q", spec, got, want)
		}
	}
}

// Discovery reads the commit's tree, never the checkout: an untracked
// package.json that a glob would match, and an uncommitted edit to a tracked
// one, change nothing. The same tree read again gives the same digest.
func TestDiscoverAt_ReadsTheTreeNotTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "config", "user.email", "warden-test")
	gitT(t, dir, "config", "user.name", "warden-test")
	write(t, dir, "package.json", `{"name":"root","workspaces":["packages/*"]}`)
	write(t, dir, "packages/a/package.json", `{"name":"a"}`)
	write(t, dir, "packages/b/package.json", `{"name":"b","dependencies":{"a":"*"}}`)
	gitT(t, dir, "add", "--", "package.json", "packages")
	gitT(t, dir, "commit", "-q", "-m", "workspaces")
	head := strings.TrimSpace(gitT(t, dir, "rev-parse", "HEAD"))

	before, err := DiscoverAt(dir, head)
	if err != nil {
		t.Fatalf("DiscoverAt: %v", err)
	}
	write(t, dir, "packages/untracked/package.json", `{"name":"untracked","dependencies":{"b":"*"}}`)
	write(t, dir, "packages/a/package.json", `{"name":"renamed-on-disk","dependencies":{"b":"*"}}`)
	after, err := DiscoverAt(dir, head)
	if err != nil {
		t.Fatalf("DiscoverAt after disk edits: %v", err)
	}
	want := []domain.Workspace{ws("packages/a", "a", "js"), ws("packages/b", "b", "js", "packages/a")}
	assertWorkspaces(t, after.Workspaces, want)
	d1, _ := before.Digest()
	d2, _ := after.Digest()
	if d1 != d2 {
		t.Fatalf("graph digest moved with untracked files: %s != %s", d1, d2)
	}
	if _, err := DiscoverAt(dir, "no-such-rev"); err == nil {
		t.Fatal("an unresolvable revision must be an error")
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(fmt.Errorf("write %s: %w", rel, err))
	}
}

// The root package is the workspace root, not a workspace: "*" does not claim
// it, though an explicit "." does (and "!." takes it back).
func TestDiscover_RootPackageOnlyWhenNamed(t *testing.T) {
	for name, tc := range map[string]struct {
		patterns string
		want     []domain.Workspace
	}{
		"star":         {`["*"]`, []domain.Workspace{ws("a", "a", "js")}},
		"explicit dot": {`["*", "."]`, []domain.Workspace{ws(".", "root", "js"), ws("a", "a", "js")}},
		"dot negated":  {`[".", "*", "!."]`, []domain.Workspace{ws("a", "a", "js")}},
	} {
		t.Run(name, func(t *testing.T) {
			tree := memTree{
				"package.json":   `{"name":"root","workspaces":` + tc.patterns + `}`,
				"a/package.json": `{"name":"a"}`,
			}
			assertWorkspaces(t, mustDiscover(t, tree), tc.want)
		})
	}
}
