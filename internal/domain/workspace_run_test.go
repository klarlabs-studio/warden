package domain

import (
	"reflect"
	"strings"
	"testing"
)

func runGraph() WorkspaceGraph {
	return WorkspaceGraph{Workspaces: []Workspace{
		{Path: "web", Name: "web", Ecosystem: "js", Deps: []string{"ui"}},
		{Path: "ui", Name: "@x/ui", Ecosystem: "js"},
		{Path: "svc", Name: "example.com/svc", Ecosystem: "go", Deps: []string{"lib"}},
		{Path: "lib", Name: "example.com/lib", Ecosystem: "go"},
	}}
}

// A run's full selection is exactly what Select returns when a rule forces
// full: every head workspace, each for the full reason.
func TestFullSelection_MatchesSelectsFullShape(t *testing.T) {
	g := runGraph()
	got, err := FullSelection(g, ForcedFull{Rule: FullRuleNoAnchor})
	if err != nil {
		t.Fatal(err)
	}
	viaSelect, err := Select(SelectionInput{Head: g, Base: g, Changed: []string{WardenConfigFile}, AnchorConfigurationDigest: "d", ConfigurationDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != SelectionFull || got.Full == nil || got.Full.Rule != FullRuleNoAnchor {
		t.Fatalf("kind %s full %+v", got.Kind, got.Full)
	}
	if !reflect.DeepEqual(got.Workspaces, viaSelect.Workspaces) || !reflect.DeepEqual(got.Paths(), g.Paths()) {
		t.Fatalf("workspaces %+v\nwant %+v", got.Workspaces, viaSelect.Workspaces)
	}
	if _, err := FullSelection(WorkspaceGraph{Workspaces: []Workspace{{Path: "../x", Name: "x"}}}, ForcedFull{Rule: FullRuleSelectAll}); err == nil {
		t.Fatal("an invalid graph must not be selected over")
	}
}

func TestWorkspaceGraph_Only(t *testing.T) {
	g := runGraph()
	g.Workspaces[0].Deps = append(g.Workspaces[0].Deps, "lib") // an edge across ecosystems
	js := g.Only([]string{"js"})
	if got := js.Paths(); !reflect.DeepEqual(got, []string{"ui", "web"}) {
		t.Fatalf("js-only paths %v", got)
	}
	if err := js.Validate(); err != nil {
		t.Fatalf("an edge into a dropped workspace must go with it: %v", err)
	}
	if got := g.Only(nil).Paths(); len(got) != 4 {
		t.Fatalf("no filter keeps every workspace: %v", got)
	}
}

func TestWorkspaceSelection_TargetsAndDescribe(t *testing.T) {
	g := runGraph()
	sel, err := Select(SelectionInput{Base: g, Head: g, Changed: []string{"ui/a.ts"}, AnchorConfigurationDigest: "d", ConfigurationDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	ws := WorkspaceSelection{Selection: sel, Graph: g}
	paths := func(xs []Workspace) []string {
		out := []string{}
		for _, w := range xs {
			out = append(out, w.Path)
		}
		return out
	}
	if got := paths(ws.Targets(nil)); !reflect.DeepEqual(got, []string{"ui", "web"}) {
		t.Fatalf("targets %v", got)
	}
	if got := paths(ws.Targets([]string{"go"})); len(got) != 0 {
		t.Fatalf("go targets %v", got)
	}
	if got := ws.Describe(); got != "affected: 2 of 4 workspace(s)" {
		t.Fatalf("describe %q", got)
	}
	full, _ := FullSelection(g, ForcedFull{Rule: FullRuleLockfile, Path: "pnpm-lock.yaml"})
	if got := (WorkspaceSelection{Selection: full, Graph: g}).Describe(); got != "all 4 workspace(s); lockfile pnpm-lock.yaml" {
		t.Fatalf("describe %q", got)
	}
}

func TestStepResult_FailedWorkspacesNamesOnlyEachFailures(t *testing.T) {
	res := StepResult{Workspaces: []WorkspaceOutcome{
		{Workspace: "a", Status: StepPass, Mode: WorkspaceModeEach},
		{Workspace: "b", Status: StepFail, Mode: WorkspaceModeEach},
	}}
	if got := res.FailedWorkspaces(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("failed %v", got)
	}
	// A failed batch does not say which workspace broke.
	batch := StepResult{Workspaces: []WorkspaceOutcome{{Workspace: "a", Status: StepFail, Mode: WorkspaceModeBatch}}}
	if got := batch.FailedWorkspaces(); len(got) != 0 {
		t.Fatalf("a batch must name no failed workspace: %v", got)
	}
}

func TestWorkspaceExecutionNotice(t *testing.T) {
	if n := WorkspaceExecutionNotice(PrePush, WorkspaceSelectAffected); !strings.Contains(n, "trusted full-scope note") || !strings.Contains(n, "refuse an affected-scope note by default") {
		t.Errorf("pre-push affected notice %q", n)
	}
	if n := WorkspaceExecutionNotice(PrePush, WorkspaceSelectAll); !strings.Contains(n, "every discovered workspace at pre-push") || !strings.Contains(n, "records no workspace scope") {
		t.Errorf("pre-push all notice %q", n)
	}
	if n := WorkspaceExecutionNotice(PreCommit, WorkspaceSelectAffected); !strings.Contains(n, "staged change affects") {
		t.Errorf("pre-commit affected notice %q", n)
	}
	if n := WorkspaceExecutionNotice(PreCommit, WorkspaceSelectAll); !strings.Contains(n, "select: all") {
		t.Errorf("pre-commit all notice %q", n)
	}
}

// Settings read only for repo-wide command steps would be silent no-ops on a
// per-workspace step; the merged config refuses them, wherever each half
// was declared.
func TestValidateEffective_RefusesSettingsAPerWorkspaceStepIgnores(t *testing.T) {
	base := func() Config {
		return Config{
			Workspaces:   &WorkspacesConfig{Discover: []string{"js"}},
			PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "pnpm test"}},
		}
	}
	if err := base().ValidateEffective(); err != nil {
		t.Fatalf("plain config: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"cache.test":       func(c *Config) { c.Cache = map[string][]string{"test": {"**/*.ts"}} },
		"predictive_cache": func(c *Config) { c.PredictiveCache = []string{"test"} },
		"diagnostics.test": func(c *Config) { c.Diagnostics = map[string]string{"test": "out.sarif"} },
	} {
		c := base()
		mutate(&c)
		err := c.ValidateEffective()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want a load error naming it, got %v", name, err)
		}
	}
}
