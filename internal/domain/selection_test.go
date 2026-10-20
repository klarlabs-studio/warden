package domain

import (
	"reflect"
	"strings"
	"testing"
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
		{"invalid base", func() SelectionInput { in := input(); in.Base = graph(ws("a", "a", "a")); return in }(), "base workspace graph"},
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
