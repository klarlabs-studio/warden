package workspace

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// Nx projects in a package-manager workspace are JS workspaces already; an
// Nx `implicitDependencies` entry, in project.json or package.json "nx", adds
// an edge to the project it names (by Nx project name or package name, or by
// a glob over them). A "!" entry, which removes an edge Nx inferred from
// source, removes nothing here: warden never infers that edge, and dropping
// a declared one would shrink a selection.
func TestDiscover_NxImplicitDependencies(t *testing.T) {
	tree := memTree{
		"nx.json":                    `{"targetDefaults":{}}`,
		"pnpm-workspace.yaml":        "packages: [\"libs/*\", \"apps/*\"]\n",
		"libs/ui/package.json":       `{"name":"@fx/ui"}`,
		"libs/ui/project.json":       `{"name":"ui"}`,
		"libs/data/package.json":     `{"name":"@fx/data","nx":{"name":"data"}}`,
		"libs/shared-a/package.json": `{"name":"shared-a"}`,
		"libs/shared-b/package.json": `{"name":"shared-b"}`,
		"apps/web/package.json":      `{"name":"web"}`,
		"apps/web/project.json":      `{"name":"web-app","implicitDependencies":["ui","shared-*","!@fx/data","not-a-project"]}`,
		"apps/admin/package.json":    `{"name":"admin","nx":{"implicitDependencies":["@fx/ui","data"]},"dependencies":{"web":"workspace:*"}}`,
		"apps/e2e/package.json":      `{"name":"e2e"}`,
		"apps/e2e/project.json":      `{"implicitDependencies":["web-app"]}`,
		// Outside every workspace glob: not a workspace, and Nx-only
		// projects are not discovered on their own.
		"tools/gen/project.json": `{"name":"gen","implicitDependencies":["ui"]}`,
	}
	assertWorkspaces(t, mustDiscover(t, tree), []domain.Workspace{
		ws("apps/admin", "admin", EcosystemJS, "apps/web", "libs/data", "libs/ui"),
		ws("apps/e2e", "e2e", EcosystemJS, "apps/web"),
		ws("apps/web", "web", EcosystemJS, "libs/shared-a", "libs/shared-b", "libs/ui"),
		ws("libs/data", "@fx/data", EcosystemJS),
		ws("libs/shared-a", "shared-a", EcosystemJS),
		ws("libs/shared-b", "shared-b", EcosystemJS),
		ws("libs/ui", "@fx/ui", EcosystemJS),
	})
}

func TestDiscover_NxMalformed(t *testing.T) {
	cases := map[string]struct {
		tree memTree
		want string
	}{
		"project.json is not JSON": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":"a"}`, "a/project.json": `{"name":`}, "a/project.json:"},
		"implicitDependencies is not a list": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":"a"}`, "a/project.json": `{"implicitDependencies":"b"}`}, `a/project.json: "implicitDependencies" must be a list of project names`},
		"package.json nx is not an object": {
			memTree{"package.json": `{"workspaces":["a"]}`, "a/package.json": `{"name":"a","nx":[]}`}, `a/package.json: "nx" must be an object`},
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
