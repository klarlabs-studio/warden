package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const cfg = "sha256:cfg"

// mono is a small pnpm-shaped repo: web depends on ui, ui on util; api stands
// alone.
func mono() WorkspaceGraph {
	return graph(
		ws("apps/web", "web", "packages/ui"),
		ws("packages/ui", "@acme/ui", "packages/util"),
		ws("packages/util", "@acme/util"),
		Workspace{Path: "services/api", Name: "api", Ecosystem: "go"},
	)
}

func input(changed ...string) SelectionInput {
	return SelectionInput{
		Base: mono(), Head: mono(), Changed: changed,
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	}
}

func mustSelect(t *testing.T, in SelectionInput) Selection {
	t.Helper()
	s, err := Select(in)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	return s
}

func TestSelectAffected(t *testing.T) {
	cases := []struct {
		name string
		in   SelectionInput
		want []string
	}{
		{"leaf change reaches dependents", input("packages/util/index.ts"), []string{"apps/web", "packages/ui", "packages/util"}},
		{"middle change", input("packages/ui/button.tsx"), []string{"apps/web", "packages/ui"}},
		{"top change stays local", input("apps/web/page.tsx"), []string{"apps/web"}},
		{"independent workspace", input("services/api/main.go"), []string{"services/api"}},
		{"no change selects nothing", input(), []string{}},
		{"workspace directory itself (gitlink)", input("services/api"), []string{"services/api"}},
		// A rename is a delete plus an add: both owners are selected.
		{"rename across workspaces", input("packages/ui/x.ts", "services/api/x.ts"), []string{"apps/web", "packages/ui", "services/api"}},
		// A lockfile inside a workspace belongs to it.
		{"nested lockfile is owned", input("services/api/go.sum", "apps/web/package-lock.json"), []string{"apps/web", "services/api"}},
		// An owned path is selected even when an ignore glob matches it.
		{"ignore does not exempt owned paths", func() SelectionInput {
			in := input("packages/util/README.md")
			in.Rules.Ignore = []string{"**/*.md"}
			return in
		}(), []string{"apps/web", "packages/ui", "packages/util"}},
		{"ignored unowned path", func() SelectionInput {
			in := input("docs/guide.md", "services/api/main.go")
			in.Rules.Ignore = []string{"docs/**"}
			return in
		}(), []string{"services/api"}},
		{"unowned mode ignore", func() SelectionInput {
			in := input("Makefile", "apps/web/x")
			in.Rules.Unowned = UnownedIgnore
			return in
		}(), []string{"apps/web"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustSelect(t, c.in)
			if s.Kind != SelectionAffected || s.Full != nil {
				t.Fatalf("Kind = %s, Full = %+v; want affected", s.Kind, s.Full)
			}
			if got := s.Paths(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("selected %v, want %v", got, c.want)
			}
			for _, w := range s.Workspaces {
				if len(w.Reasons) == 0 {
					t.Errorf("%s selected without a reason", w.Path)
				}
			}
		})
	}
}

func TestSelectReasons(t *testing.T) {
	s := mustSelect(t, input("packages/util/a.ts", "packages/util/b.ts", "packages/ui/c.ts"))
	want := []SelectedWorkspace{
		{Path: "apps/web", Name: "web", Reasons: []SelectionReason{{Kind: ReasonDependent, Via: []string{"packages/ui"}}}},
		{Path: "packages/ui", Name: "@acme/ui", Reasons: []SelectionReason{
			{Kind: ReasonChanged, Paths: []string{"packages/ui/c.ts"}},
			{Kind: ReasonDependent, Via: []string{"packages/util"}},
		}},
		{Path: "packages/util", Name: "@acme/util", Reasons: []SelectionReason{
			{Kind: ReasonChanged, Paths: []string{"packages/util/a.ts", "packages/util/b.ts"}},
		}},
	}
	if !reflect.DeepEqual(s.Workspaces, want) {
		t.Fatalf("workspaces =\n%+v\nwant\n%+v", s.Workspaces, want)
	}
}

func TestSelectForcesFull(t *testing.T) {
	withRules := func(in SelectionInput, r SelectionRules) SelectionInput { in.Rules = r; return in }
	cases := []struct {
		name string
		in   SelectionInput
		want ForcedFull
	}{
		{"config file changed", input(".warden.yaml"), ForcedFull{Rule: FullRuleConfiguration, Path: ".warden.yaml"}},
		{"config digest differs (extends base moved)", func() SelectionInput {
			in := input("apps/web/x")
			in.ConfigurationDigest = "sha256:other"
			return in
		}(), ForcedFull{Rule: FullRuleConfiguration}},
		{"anchor config digest unknown", func() SelectionInput {
			in := input("apps/web/x")
			in.AnchorConfigurationDigest = ""
			return in
		}(), ForcedFull{Rule: FullRuleConfiguration}},
		{"both config digests unknown", func() SelectionInput {
			in := input("apps/web/x")
			in.AnchorConfigurationDigest, in.ConfigurationDigest = "", ""
			return in
		}(), ForcedFull{Rule: FullRuleConfiguration}},
		{"absolute path", input("/etc/passwd"), ForcedFull{Rule: FullRuleInvalidPath, Path: "/etc/passwd"}},
		{"escaping path", input("../x"), ForcedFull{Rule: FullRuleInvalidPath, Path: "../x"}},
		{"unclean path", input("apps/web/../../x"), ForcedFull{Rule: FullRuleInvalidPath, Path: "apps/web/../../x"}},
		{"empty path", input(""), ForcedFull{Rule: FullRuleInvalidPath}},
		{"newline in path", input("apps/web/a\nb"), ForcedFull{Rule: FullRuleInvalidPath, Path: "apps/web/a\nb"}},
		{"root package.json", input("package.json"), ForcedFull{Rule: FullRuleRootManifest, Path: "package.json"}},
		{"pnpm-workspace.yaml", input("pnpm-workspace.yaml"), ForcedFull{Rule: FullRuleRootManifest, Path: "pnpm-workspace.yaml"}},
		{"go.work", input("go.work"), ForcedFull{Rule: FullRuleRootManifest, Path: "go.work"}},
		{"root lockfile", input("pnpm-lock.yaml", "apps/web/x"), ForcedFull{Rule: FullRuleLockfile, Path: "pnpm-lock.yaml"}},
		{"go.work.sum", input("go.work.sum"), ForcedFull{Rule: FullRuleLockfile, Path: "go.work.sum"}},
		{"default global", input("turbo.json"), ForcedFull{Rule: FullRuleGlobal, Path: "turbo.json", Pattern: "turbo.json"}},
		{"user global", withRules(input("tools/build/x.sh"), SelectionRules{Global: []string{"tools/build/**"}}),
			ForcedFull{Rule: FullRuleGlobal, Path: "tools/build/x.sh", Pattern: "tools/build/**"}},
		// Ignore never exempts a global, lockfile or config path.
		{"ignore cannot exempt lockfile", withRules(input("yarn.lock"), SelectionRules{Ignore: []string{"**"}}),
			ForcedFull{Rule: FullRuleLockfile, Path: "yarn.lock"}},
		{"ignore cannot exempt global", withRules(input(".npmrc"), SelectionRules{Ignore: []string{"**"}, Unowned: UnownedIgnore}),
			ForcedFull{Rule: FullRuleGlobal, Path: ".npmrc", Pattern: ".npmrc"}},
		{"ignore cannot exempt config", withRules(input(".warden.yaml"), SelectionRules{Ignore: []string{"**"}, Unowned: UnownedIgnore}),
			ForcedFull{Rule: FullRuleConfiguration, Path: ".warden.yaml"}},
		{"unowned path", input("Makefile", "apps/web/x"), ForcedFull{Rule: FullRuleUnowned, Path: "Makefile"}},
		{"unowned explicit full", withRules(input(".github/workflows/ci.yml"), SelectionRules{Unowned: UnownedFull}),
			ForcedFull{Rule: FullRuleUnowned, Path: ".github/workflows/ci.yml"}},
		// Paths git renders for renames or quotes are not real paths; they
		// match no workspace and must not be read as "nothing changed".
		{"numstat rename rendering", input("packages/{ui => util}/x.ts"), ForcedFull{Rule: FullRuleUnowned, Path: "packages/{ui => util}/x.ts"}},
		{"C-quoted path", input(`"apps/web/\303\251.ts"`), ForcedFull{Rule: FullRuleInvalidPath, Path: `"apps/web/\303\251.ts"`}},
		{"quoted path", input(`"apps/web/a b.ts"`), ForcedFull{Rule: FullRuleUnowned, Path: `"apps/web/a b.ts"`}},
		// A sibling directory sharing a prefix is not owned.
		{"prefix is not ownership", input("apps/web2/x"), ForcedFull{Rule: FullRuleUnowned, Path: "apps/web2/x"}},
		// First rule wins, whatever order the paths came in.
		{"precedence", input("Makefile", "turbo.json", "yarn.lock", "go.work", ".warden.yaml"),
			ForcedFull{Rule: FullRuleConfiguration, Path: ".warden.yaml"}},
		{"precedence without config", input("Makefile", "turbo.json", "yarn.lock", "go.work"),
			ForcedFull{Rule: FullRuleRootManifest, Path: "go.work"}},
	}
	all := mono().Paths()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustSelect(t, c.in)
			if s.Kind != SelectionFull || s.Full == nil {
				t.Fatalf("Kind = %s, want full", s.Kind)
			}
			if *s.Full != c.want {
				t.Fatalf("Full = %+v, want %+v", *s.Full, c.want)
			}
			if got := s.Paths(); !reflect.DeepEqual(got, all) {
				t.Fatalf("full selection = %v, want every workspace %v", got, all)
			}
			for _, w := range s.Workspaces {
				if len(w.Reasons) != 1 || w.Reasons[0].Kind != ReasonFull {
					t.Errorf("%s reasons = %+v, want [full]", w.Path, w.Reasons)
				}
			}
		})
	}
}

// nested is a repo with a root workspace and nested ones, including the
// "a" / "a-b" / "a/c" layout whose sort order ("a-b" between "a" and "a/c")
// trips neighbor-comparison shortcuts.
func nested() WorkspaceGraph {
	return graph(
		Workspace{Path: ".", Name: "example.com/root", Ecosystem: "go"},
		ws("a", "a"),
		ws("a-b", "ab"),
		ws("a/c", "c"),
		ws("a/c/d", "d", "a"), // depends on its grandparent
		ws("tools", "tools", "."),
	)
}

func nestedInput(changed ...string) SelectionInput {
	in := input(changed...)
	in.Base, in.Head = nested(), nested()
	return in
}

func TestSelectLongestPrefixOwnership(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"nested owner wins", []string{"a/c/x.go"}, []string{"a/c"}},
		{"sibling sharing a prefix", []string{"a-b/x.go"}, []string{"a-b"}},
		{"parent keeps the rest", []string{"a/x.go"}, []string{"a", "a/c/d"}},
		{"deepest owner", []string{"a/c/d/e/f.go"}, []string{"a/c/d"}},
		{"workspace directory itself", []string{"a/c"}, []string{"a/c"}},
		// The root workspace owns what nobody else does, at lowest priority,
		// and its dependents (tools uses the root module) follow.
		{"root owns the rest", []string{"internal/x.go", "README.md"}, []string{".", "tools"}},
		{"root files beside nested ones", []string{"cmd/main.go", "a-b/y.go"}, []string{".", "a-b", "tools"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustSelect(t, nestedInput(c.changed...))
			if s.Kind != SelectionAffected {
				t.Fatalf("Kind = %s (%+v), want affected", s.Kind, s.Full)
			}
			if got := s.Paths(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("selected %v, want %v", got, c.want)
			}
		})
	}
}

// With a root workspace no path is unowned, but the rules checked before
// ownership still force full: the config file, root manifests (including a
// root module's go.mod), root lockfiles (including its go.sum) and globals.
func TestSelectRootWorkspaceStillForcesFull(t *testing.T) {
	cases := []struct {
		path string
		want FullRule
	}{
		{".warden.yaml", FullRuleConfiguration},
		{"go.work", FullRuleRootManifest},
		{"go.mod", FullRuleRootManifest},
		{"package.json", FullRuleRootManifest},
		{"go.work.sum", FullRuleLockfile},
		{"go.sum", FullRuleLockfile},
		{"pnpm-lock.yaml", FullRuleLockfile},
		{"turbo.json", FullRuleGlobal},
		{"/abs", FullRuleInvalidPath},
	}
	all := nested().Paths()
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			s := mustSelect(t, nestedInput("a/c/x.go", c.path))
			if s.Kind != SelectionFull || s.Full.Rule != c.want {
				t.Fatalf("Kind = %s, Full = %+v; want full by %s", s.Kind, s.Full, c.want)
			}
			if !reflect.DeepEqual(s.Paths(), all) {
				t.Fatalf("selected %v, want %v", s.Paths(), all)
			}
		})
	}
	// Below the root, go.mod and go.sum are the owning workspace's files.
	s := mustSelect(t, nestedInput("a/c/go.mod", "a/c/go.sum"))
	if s.Kind != SelectionAffected || !reflect.DeepEqual(s.Paths(), []string{"a/c"}) {
		t.Fatalf("nested go.mod: Kind = %s, selected %v", s.Kind, s.Paths())
	}
	// And no unowned path exists, so unowned never forces full.
	s = mustSelect(t, nestedInput(".github/workflows/ci.yml", "Makefile"))
	if s.Kind != SelectionAffected || !reflect.DeepEqual(s.Paths(), []string{".", "tools"}) {
		t.Fatalf("root-owned paths: Kind = %s (%+v), selected %v", s.Kind, s.Full, s.Paths())
	}
}

// A workspace the change deletes still reaches what depended on it, and a
// deleted edge still carries a change in the old dependency.
func TestSelectDeletedWorkspaceReachesDependents(t *testing.T) {
	base := graph(
		ws("apps/web", "web", "packages/legacy"),
		ws("apps/admin", "admin", "packages/legacy"),
		ws("packages/legacy", "legacy"),
		ws("services/api", "api"),
	)
	head := graph(ws("apps/web", "web"), ws("apps/admin", "admin"), ws("services/api", "api"))
	in := SelectionInput{
		Base: base, Head: head,
		Changed:                   []string{"packages/legacy/index.ts", "packages/legacy/package.json"},
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	}
	s := mustSelect(t, in)
	if s.Kind != SelectionAffected {
		t.Fatalf("Kind = %s (%+v)", s.Kind, s.Full)
	}
	if got, want := s.Paths(), []string{"apps/admin", "apps/web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
	if !reflect.DeepEqual(s.Removed, []string{"packages/legacy"}) {
		t.Fatalf("Removed = %v", s.Removed)
	}
	for _, w := range s.Workspaces {
		want := []SelectionReason{{Kind: ReasonDependent, Via: []string{"packages/legacy"}}}
		if !reflect.DeepEqual(w.Reasons, want) {
			t.Errorf("%s reasons = %+v", w.Path, w.Reasons)
		}
	}
}

func TestSelectDeletedEdgeStillCarries(t *testing.T) {
	base := mono()
	head := mono()
	head.Workspaces[0].Deps = nil // web no longer depends on ui
	in := SelectionInput{
		Base: base, Head: head, Changed: []string{"packages/ui/x.ts", "apps/web/package.json"},
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	}
	s := mustSelect(t, in)
	if got, want := s.Paths(), []string{"apps/web", "packages/ui"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
	// A new workspace in head is owned by head.
	in = SelectionInput{
		Base: mono(), Head: graph(append(mono().Workspaces, ws("packages/new", "new"))...),
		Changed:                   []string{"packages/new/index.ts"},
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	}
	if got := mustSelect(t, in).Paths(); !reflect.DeepEqual(got, []string{"packages/new"}) {
		t.Fatalf("new workspace: selected %v", got)
	}
}

// cyclic is a repo with every shape of dependency cycle: a and b depend on
// each other and web on a; c -> d -> e -> c with svc on e; s on itself; lone
// on nothing.
func cyclic() WorkspaceGraph {
	return graph(
		ws("pkg/a", "a", "pkg/b"),
		ws("pkg/b", "b", "pkg/a"),
		ws("apps/web", "web", "pkg/a"),
		ws("pkg/c", "c", "pkg/d"),
		ws("pkg/d", "d", "pkg/e"),
		ws("pkg/e", "e", "pkg/c"),
		ws("apps/svc", "svc", "pkg/e"),
		ws("pkg/s", "s", "pkg/s"),
		ws("pkg/lone", "lone"),
	)
}

// selectWithin runs Select with a deadline, so a closure that loops on a
// cycle fails the test instead of hanging the suite.
func selectWithin(t *testing.T, in SelectionInput) Selection {
	t.Helper()
	type result struct {
		s   Selection
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := Select(in)
		done <- result{s, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Select: %v", r.err)
		}
		return r.s
	case <-time.After(5 * time.Second):
		t.Fatal("Select did not return within 5s: the dependent closure loops on a cycle")
		return Selection{}
	}
}

// A change in any member of a cycle selects every member, plus everything
// that depends on the cycle. A self-dependency selects only the workspace
// itself and never names it as its own reason.
func TestSelectCycles(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		want    map[string][]SelectionReason
	}{
		{"two-cycle", []string{"pkg/b/x.ts"}, map[string][]SelectionReason{
			"apps/web": {{Kind: ReasonDependent, Via: []string{"pkg/a"}}},
			"pkg/a":    {{Kind: ReasonDependent, Via: []string{"pkg/b"}}},
			"pkg/b":    {{Kind: ReasonChanged, Paths: []string{"pkg/b/x.ts"}}, {Kind: ReasonDependent, Via: []string{"pkg/a"}}},
		}},
		{"three-cycle", []string{"pkg/d/x.ts"}, map[string][]SelectionReason{
			"apps/svc": {{Kind: ReasonDependent, Via: []string{"pkg/e"}}},
			"pkg/c":    {{Kind: ReasonDependent, Via: []string{"pkg/d"}}},
			"pkg/d":    {{Kind: ReasonChanged, Paths: []string{"pkg/d/x.ts"}}, {Kind: ReasonDependent, Via: []string{"pkg/e"}}},
			"pkg/e":    {{Kind: ReasonDependent, Via: []string{"pkg/c"}}},
		}},
		{"self dependency", []string{"pkg/s/x.ts"}, map[string][]SelectionReason{
			"pkg/s": {{Kind: ReasonChanged, Paths: []string{"pkg/s/x.ts"}}},
		}},
		{"dependent of a cycle stays local", []string{"apps/web/x.ts"}, map[string][]SelectionReason{
			"apps/web": {{Kind: ReasonChanged, Paths: []string{"apps/web/x.ts"}}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := selectWithin(t, SelectionInput{
				Base: cyclic(), Head: cyclic(), Changed: c.changed,
				AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
			})
			if s.Kind != SelectionAffected {
				t.Fatalf("Kind = %s (%+v), want affected", s.Kind, s.Full)
			}
			got := map[string][]SelectionReason{}
			for _, w := range s.Workspaces {
				got[w.Path] = w.Reasons
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("selected\n%+v\nwant\n%+v", got, c.want)
			}
		})
	}
}

// A cycle that exists only across the two graphs (a -> b in the base, b -> a
// in the head) is still a cycle of the union, and the closure terminates.
func TestSelectCycleAcrossGraphs(t *testing.T) {
	base := graph(ws("pkg/a", "a", "pkg/b"), ws("pkg/b", "b"), ws("apps/web", "web", "pkg/b"))
	head := graph(ws("pkg/a", "a"), ws("pkg/b", "b", "pkg/a"), ws("apps/web", "web"))
	s := selectWithin(t, SelectionInput{
		Base: base, Head: head, Changed: []string{"pkg/a/x.ts"},
		AnchorConfigurationDigest: cfg, ConfigurationDigest: cfg,
	})
	if got, want := s.Paths(), []string{"apps/web", "pkg/a", "pkg/b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
}

func TestSelectRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		in   SelectionInput
		want string
	}{
		{"bad unowned", func() SelectionInput { in := input(); in.Rules.Unowned = "skip"; return in }(), "workspaces.unowned"},
		{"empty global", func() SelectionInput { in := input(); in.Rules.Global = []string{""}; return in }(), "workspaces.global: empty glob"},
		{"absolute ignore", func() SelectionInput { in := input(); in.Rules.Ignore = []string{"/docs"}; return in }(), "workspaces.ignore"},
		{"control char glob", func() SelectionInput { in := input(); in.Rules.Ignore = []string{"a\nb"}; return in }(), "control character"},
		{"long glob", func() SelectionInput {
			in := input()
			in.Rules.Global = []string{strings.Repeat("a", maxGlobLen+1)}
			return in
		}(), "longer than"},
		{"invalid head", func() SelectionInput { in := input(); in.Head = graph(ws("a", "a;b")); return in }(), "head workspace graph"},
		{"invalid base", func() SelectionInput { in := input(); in.Base = graph(ws("a", "a", "missing")); return in }(), "base workspace graph"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Select(c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Select() error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// goldenSelectionDigest pins the selection encoding: bump SelectionVersion
// rather than editing it.
const goldenSelectionDigest = "sha256:7b8c89e25c3a0d87734d11d6e5e1dd06717e93b12c578d4d11818888f9d3b626"

func TestSelectionDigestGolden(t *testing.T) {
	in := input("packages/ui/button.tsx", "docs/x.md")
	in.Rules = SelectionRules{Global: []string{"tools/**"}, Ignore: []string{"docs/**"}}
	s := mustSelect(t, in)
	got, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenSelectionDigest {
		t.Fatalf("selection digest = %s, want %s", got, goldenSelectionDigest)
	}

	// The same inputs in another order digest the same.
	in2 := input("docs/x.md", "packages/ui/button.tsx", "docs/x.md")
	in2.Rules = SelectionRules{Global: []string{"tools/**", "tools/**"}, Ignore: []string{"docs/**"}, Unowned: UnownedFull}
	hw := in2.Head.Workspaces
	hw[0], hw[3] = hw[3], hw[0]
	got2, err := mustSelect(t, in2).Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got2 != got {
		t.Fatalf("selection digest depends on input order: %s != %s", got2, got)
	}
}
