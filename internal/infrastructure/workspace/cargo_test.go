package workspace

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// A Cargo workspace with every way a member is declared and every way one
// member names another: glob and literal members, an exclude, the root
// package, a path dependency Cargo makes a member implicitly, and edges
// through path, workspace inheritance, target-specific tables, the
// underscore spellings and [patch].
func TestDiscover_Cargo(t *testing.T) {
	tree := memTree{
		"Cargo.toml": `
[package]
name = "fx"
version = "0.1.0"

[workspace]
members = ["crates/*", "./tools/xtask/", "nested/**"]
exclude = ["crates/legacy", "nested/skip"]

[workspace.dependencies]
core = { package = "fx-core", path = "crates/core", version = "0.1" }
serde = "1"

[patch.crates-io]
fx-macros = { path = "crates/macros" }
outside = { path = "../outside" }

[dependencies]
fx-cli = { path = "crates/cli" }
`,
		"crates/core/Cargo.toml": `[package]
name = "fx-core"
[dependencies]
serde = { workspace = true }
`,
		"crates/cli/Cargo.toml": `[package]
name = "fx-cli"
[dependencies]
core = { workspace = true, features = ["std"] }
fx-macros = "0.1"
[target.'cfg(unix)'.dev-dependencies]
fx-util = { path = "../../support/util" }
[build_dependencies]
fx-build = { path = "../../nested/deep/build" }
`,
		"crates/macros/Cargo.toml": `[package]
name = "fx-macros"
`,
		"crates/legacy/Cargo.toml": `[package]
name = "fx-legacy"
[dependencies]
fx-core = { path = "../core" }
`,
		"crates/docs/README.md":        "a glob match without Cargo.toml is not a crate",
		"tools/xtask/Cargo.toml":       "[package]\nname = \"xtask\"\n[dev-dependencies]\ncore.workspace = true\nfar = { path = \"../../../far\" }\n",
		"nested/deep/build/Cargo.toml": "[package]\nname = \"fx-build\"\n",
		"nested/skip/Cargo.toml":       "[package]\nname = \"skipped\"\n",
		// Not matched by members; a member's path dependency makes it one.
		"support/util/Cargo.toml": "[package]\nname = \"fx-util\"\n[dependencies]\nfx-core = { path = \"../../crates/core\" }\n",
		// Neither matched nor depended on.
		"examples/demo/Cargo.toml": "[package]\nname = \"demo\"\n",
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws(".", "fx", EcosystemRust, "crates/cli"),
		ws("crates/cli", "fx-cli", EcosystemRust, "crates/core", "crates/macros", "nested/deep/build", "support/util"),
		ws("crates/core", "fx-core", EcosystemRust),
		ws("crates/macros", "fx-macros", EcosystemRust),
		ws("nested/deep/build", "fx-build", EcosystemRust),
		ws("support/util", "fx-util", EcosystemRust, "crates/core"),
		ws("tools/xtask", "xtask", EcosystemRust, "crates/core"),
	})
}

// A virtual manifest has no root package, so "." is no workspace. An exclude
// does not remove a member listed by its own path, which is how Cargo reads
// it, and a [replace] entry links like a [patch] one.
func TestDiscover_CargoVirtualManifestAndExplicitMembers(t *testing.T) {
	tree := memTree{
		"Cargo.toml": `[workspace]
members = ["libs/a", "libs/b"]
exclude = ["libs"]
[replace]
"b:0.1.0" = { path = "libs/b" }
`,
		"libs/a/Cargo.toml": "[package]\nname = \"a\"\n[dependencies]\nb = \"0.1.0\"\n",
		"libs/b/Cargo.toml": "[package]\nname = \"b\"\n",
		"libs/c/Cargo.toml": "[package]\nname = \"c\"\n",
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws("libs/a", "a", EcosystemRust, "libs/b"),
		ws("libs/b", "b", EcosystemRust),
	})
}

// A path dependency that is excluded or leaves the repository is not a
// member and links nothing; a registry dependency named like a member links
// nothing either.
func TestDiscover_CargoPathDependenciesThatAreNotEdges(t *testing.T) {
	tree := memTree{
		"Cargo.toml": "[workspace]\nmembers = [\"app\", \"lib\"]\nexclude = [\"vendor\"]\n",
		"app/Cargo.toml": `[package]
name = "app"
[dependencies]
lib = "1"
vendored = { path = "../vendor/vendored" }
sibling = { path = "../../sibling-repo/crate" }
abs = { path = "/opt/crate" }
`,
		"lib/Cargo.toml":             "[package]\nname = \"lib\"\n",
		"vendor/vendored/Cargo.toml": "[package]\nname = \"vendored\"\n",
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws("app", "app", EcosystemRust),
		ws("lib", "lib", EcosystemRust),
	})
}

func TestDiscover_CargoNoWorkspace(t *testing.T) {
	for name, tree := range map[string]memTree{
		"single crate":            {"Cargo.toml": "[package]\nname = \"one\"\n", "sub/Cargo.toml": "[package]\nname = \"sub\"\n"},
		"workspace below root":    {"rust/Cargo.toml": "[workspace]\nmembers = [\"*\"]\n", "rust/a/Cargo.toml": "[package]\nname = \"a\"\n"},
		"workspace without crate": {"Cargo.toml": "[workspace]\nmembers = []\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mustDiscover(t, tree); len(got) != 0 {
				t.Fatalf("got %+v, want no workspaces", got)
			}
		})
	}
}

func TestDiscover_CargoMalformedManifestsAreErrors(t *testing.T) {
	cases := map[string]struct {
		tree memTree
		want string
	}{
		"root Cargo.toml is not TOML": {
			memTree{"Cargo.toml": "[workspace\n"}, "Cargo.toml:"},
		"members is not an array": {
			memTree{"Cargo.toml": "[workspace]\nmembers = \"a\"\n"}, "Cargo.toml:"},
		"member Cargo.toml is not TOML": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package\n"}, "a/Cargo.toml:"},
		"member has no package": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[dependencies]\n"}, `a/Cargo.toml: a workspace member needs [package] name`},
		"package name is not a string": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = 7\n"}, "a/Cargo.toml:"},
		"literal member without Cargo.toml": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"missing\"]\n"}, "the tree has no missing/Cargo.toml"},
		"path dependency without Cargo.toml": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = \"a\"\n[dependencies]\nb = { path = \"../b\" }\n"},
			"a/Cargo.toml: dependency \"b\" has path \"../b\", but the tree has no b/Cargo.toml"},
		"dependency is neither a version nor a table": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = \"a\"\n[dependencies]\nb = 1\n"},
			`a/Cargo.toml: dependency "b" must be a version string or a table`},
		"dependency path is not a string": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = \"a\"\n[dependencies]\nb = { path = 1 }\n"},
			`a/Cargo.toml: dependency "b": "path" must be a string`},
		"workspace inheritance without a workspace entry": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = \"a\"\n[dependencies]\nb = { workspace = true }\n"},
			`a/Cargo.toml: dependency "b" inherits from [workspace.dependencies], which has no "b"`},
		"bracket member pattern": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"crates/[ab]\"]\n"}, "brace, bracket"},
		"member pattern leaves the repository": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"../x\"]\n"}, "leaves the repository"},
		"exclude leaves the repository": {
			memTree{"Cargo.toml": "[workspace]\nmembers = []\nexclude = [\"../x\"]\n"}, "leaves the repository"},
		"oversized manifest": {
			memTree{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n", "a/Cargo.toml": "[package]\nname = \"a\"\ndescription = \"" + strings.Repeat("x", maxManifestBytes) + "\"\n"},
			"larger than"},
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

// A crate name or member path outside the domain's character rules fails the
// whole discovery with the validation's reason.
func TestDiscover_CargoInvalidGraph(t *testing.T) {
	tree := memTree{
		"Cargo.toml":   "[workspace]\nmembers = [\"a\", \"b\"]\n",
		"a/Cargo.toml": "[package]\nname = \"same\"\n",
		"b/Cargo.toml": "[package]\nname = \"same\"\n",
	}
	if _, err := Discover(tree); err == nil || !strings.Contains(err.Error(), `workspace name "same" is used by both`) {
		t.Fatalf("Discover error = %v, want a duplicate-name error", err)
	}
}

// Discovery runs only the ecosystems it is asked for, so a repository that
// discovers JS is not failed by a Cargo.toml it never asked to read — and
// cannot collide with a crate that shares a JS package's directory.
func TestDiscover_OnlyTheRequestedEcosystems(t *testing.T) {
	tree := memTree{
		"package.json":            `{"name":"root","workspaces":["packages/*"]}`,
		"packages/a/package.json": `{"name":"a"}`,
		// A napi-style crate in the same directory as the JS package, and
		// a broken one: neither matters unless rust is discovered.
		"Cargo.toml":            "[workspace]\nmembers = [\"packages/*\", \"broken\"]\n",
		"packages/a/Cargo.toml": "[package]\nname = \"a-native\"\n",
		"broken/Cargo.toml":     "[package\n",
	}
	g, err := Discover(tree, EcosystemJS)
	if err != nil {
		t.Fatalf("Discover(js): %v", err)
	}
	assertWorkspaces(t, g.Workspaces, []domain.Workspace{ws("packages/a", "a", EcosystemJS)})
	if _, err := Discover(tree, EcosystemRust); err == nil || !strings.Contains(err.Error(), "broken/Cargo.toml") {
		t.Fatalf("Discover(rust) error = %v, want the broken manifest named", err)
	}
	if _, err := Discover(tree, "cobol"); err == nil || !strings.Contains(err.Error(), `unknown ecosystem "cobol"`) {
		t.Fatalf("Discover(cobol) error = %v", err)
	}
	delete(tree, "broken/Cargo.toml")
	tree["Cargo.toml"] = "[workspace]\nmembers = [\"packages/*\"]\n"
	if _, err := Discover(tree); err == nil || !strings.Contains(err.Error(), `workspace path "packages/a" is declared twice`) {
		t.Fatalf("Discover(all) error = %v, want the shared directory refused", err)
	}
}
