package domain

import (
	"strings"
	"testing"
)

func ws(path, name string, deps ...string) Workspace {
	return Workspace{Path: path, Name: name, Ecosystem: "js", Deps: deps}
}

func graph(w ...Workspace) WorkspaceGraph { return WorkspaceGraph{Workspaces: w} }

func TestWorkspaceGraphValidateAccepts(t *testing.T) {
	g := graph(
		ws("packages/ui", "@acme/ui"),
		ws("apps/web", "web", "packages/ui", "packages/ui"), // duplicate edge is harmless
		Workspace{Path: "modules/api", Name: "github.com/acme/mono/api", Ecosystem: "go"},
		Workspace{Path: "tools/x_y.z", Name: "x_y.z~1"}, // ecosystem optional
		ws("a-b", "ab"),
		ws("a", "a"),
		ws("a/c", "c"),     // nested workspaces are allowed
		ws("a/c/d/e", "e"), // at any depth
		ws(".", "root"),    // and so is the repository root
	)
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if err := (WorkspaceGraph{}).Validate(); err != nil {
		t.Fatalf("empty graph: Validate() = %v, want nil", err)
	}
}

// Dependency cycles are valid: real monorepos have them (two JS packages
// that devDepend on each other, Go modules that require each other), and
// selection is a reverse-dependency closure, which is well defined on a
// cyclic graph. The digest stays deterministic whatever order the cycle is
// written in.
func TestWorkspaceGraphValidateAcceptsCycles(t *testing.T) {
	cases := map[string][2]WorkspaceGraph{
		"self dependency": {graph(ws("a", "a", "a")), graph(ws("a", "a", "a", "a"))},
		"two-cycle": {
			graph(ws("a", "a", "b"), ws("b", "b", "a")),
			graph(ws("b", "b", "a"), ws("a", "a", "b", "b")),
		},
		"three-cycle with a dependent": {
			graph(ws("a", "a", "b"), ws("b", "b", "c"), ws("c", "c", "a"), ws("app", "app", "c")),
			graph(ws("app", "app", "c"), ws("c", "c", "a"), ws("b", "b", "c"), ws("a", "a", "b")),
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			if err := pair[0].Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			d1, err1 := pair[0].Digest()
			d2, err2 := pair[1].Digest()
			if err1 != nil || err2 != nil || d1 != d2 {
				t.Fatalf("Digest() = %q (%v), reordered %q (%v); want equal and no error", d1, err1, d2, err2)
			}
		})
	}
}

func TestWorkspaceGraphValidateRefuses(t *testing.T) {
	long := strings.Repeat("a", maxWorkspaceNameLen+1)
	cases := []struct {
		name string
		g    WorkspaceGraph
		want string
	}{
		// Names reach shell commands: anything outside the class is refused.
		{"name with space", graph(ws("a", "a b")), "invalid name"},
		{"name with semicolon", graph(ws("a", "a;rm")), "invalid name"},
		{"name with command substitution", graph(ws("a", "$(id)")), "invalid name"},
		{"name with backtick", graph(ws("a", "a`id`")), "invalid name"},
		{"name with quote", graph(ws("a", "a'b")), "invalid name"},
		{"name with newline", graph(ws("a", "a\nb")), "invalid name"},
		{"name like an option", graph(ws("a", "-rf")), "invalid name"},
		{"empty name", graph(ws("a", "")), "invalid name"},
		{"empty name element", graph(ws("a", "a//b")), "invalid name"},
		{"trailing slash name", graph(ws("a", "a/")), "invalid name"},
		{"double scope", graph(ws("a", "@@a")), "invalid name"},
		{"name too long", graph(ws("a", long)), "invalid name"},
		{"bad ecosystem", graph(Workspace{Path: "a", Name: "a", Ecosystem: "JS;"}), "invalid ecosystem"},
		// Paths must be clean, relative and inside the repository.
		{"empty path", graph(ws("", "a")), "empty"},
		{"absolute path", graph(ws("/a", "a")), "relative"},
		{"backslash path", graph(ws(`a\b`, "a")), "separators"},
		{"dot-slash path", graph(ws("./a", "a")), "not clean"},
		{"trailing slash path", graph(ws("a/", "a")), "not clean"},
		{"dotdot path", graph(ws("..", "a")), "leaves the repository"},
		{"dotdot prefix", graph(ws("../a", "a")), "leaves the repository"},
		{"inner dotdot", graph(ws("a/../b", "a")), "not clean"},
		{"newline path", graph(ws("a\nb", "a")), "control character"},
		{"NUL path", graph(ws("a\x00b", "a")), "control character"},
		{"option path", graph(ws("x/-rf", "a")), "starting with '-'"},
		{"path too long", graph(ws(strings.Repeat("a", maxGlobLen+1), "a")), "longer than"},
		// Graph-level checks.
		{"duplicate path", graph(ws("a", "a"), ws("a", "b")), "declared twice"},
		{"duplicate root", graph(ws(".", "a"), ws(".", "b")), "declared twice"},
		{"dot-prefixed path", graph(ws("./.", "a")), "not clean"},
		{"duplicate name", graph(ws("a", "x"), ws("b", "x")), "used by both"},
		{"unknown dependency", graph(ws("a", "a", "b")), "not a workspace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.g.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate() = %q, want it to contain %q", err, c.want)
			}
			if _, derr := c.g.Digest(); derr == nil {
				t.Fatal("Digest() of an invalid graph returned no error")
			}
		})
	}
}

// goldenGraphDigest pins the canonical encoding. If this changes, every
// recorded graph digest stops matching what a verifier recomputes: bump
// WorkspaceGraphVersion rather than editing the value.
const goldenGraphDigest = "sha256:da92d557a2f191113963797dee01b288cffe820b083e4d2553d2f675bd1e6870"

func goldenGraph() WorkspaceGraph {
	return graph(
		ws("apps/web", "web", "packages/ui", "packages/util"),
		ws("packages/ui", "@acme/ui", "packages/util"),
		Workspace{Path: "packages/util", Name: "@acme/util", Ecosystem: "js"},
		Workspace{Path: "services/api", Name: "github.com/acme/api", Ecosystem: "go"},
	)
}

func TestWorkspaceGraphDigestGolden(t *testing.T) {
	got, err := goldenGraph().Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenGraphDigest {
		t.Fatalf("graph digest = %s, want %s", got, goldenGraphDigest)
	}
}

func TestWorkspaceGraphDigestOrderIndependent(t *testing.T) {
	a, err := goldenGraph().Digest()
	if err != nil {
		t.Fatal(err)
	}
	shuffled := graph(
		Workspace{Path: "services/api", Name: "github.com/acme/api", Ecosystem: "go", Deps: []string{}},
		ws("packages/ui", "@acme/ui", "packages/util", "packages/util"),
		ws("apps/web", "web", "packages/util", "packages/ui"),
		Workspace{Path: "packages/util", Name: "@acme/util", Ecosystem: "js"},
	)
	b, err := shuffled.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("digest depends on order: %s != %s", a, b)
	}
	// And it does depend on content.
	changed := goldenGraph()
	changed.Workspaces[0].Deps = []string{"packages/ui"}
	c, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("dropping an edge did not change the digest")
	}
}

func TestWorkspaceGraphCanonicalDoesNotMutate(t *testing.T) {
	g := graph(ws("b", "b", "a", "a"), ws("a", "a"))
	_ = g.Canonical()
	if g.Workspaces[0].Path != "b" || len(g.Workspaces[0].Deps) != 2 {
		t.Fatalf("Canonical mutated its receiver: %+v", g)
	}
	if got := g.Paths(); strings.Join(got, ",") != "a,b" {
		t.Fatalf("Paths() = %v", got)
	}
}
