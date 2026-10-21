package policy

import (
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestResolve_NoWorkspacesResolvesNone(t *testing.T) {
	if got := Resolve(specConfig(), Input{Hook: domain.PrePush}).Workspaces; got != nil {
		t.Errorf("a config without workspaces must resolve none: %+v", got)
	}
}

func TestResolve_WorkspacesDefaults(t *testing.T) {
	cfg := domain.Config{
		Workspaces:   &domain.WorkspacesConfig{Discover: []string{"js"}},
		PerWorkspace: map[string]domain.PerWorkspaceStep{"test": {Each: "pnpm test"}},
	}
	got := Resolve(cfg, Input{Hook: domain.PrePush}).Workspaces
	want := &domain.ResolvedWorkspaces{
		Discover: []string{"js"},
		Select:   domain.WorkspaceSelectAll,
		Rules:    domain.SelectionRules{Unowned: domain.UnownedFull},
		Steps:    map[domain.StepName]domain.PerWorkspaceStep{"test": {Each: "pnpm test"}},
		Active:   true,
		Notice:   domain.WorkspaceExecutionNotice(domain.PrePush, domain.WorkspaceSelectAll),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved workspaces:\n got  %+v\n want %+v", got, want)
	}
}

// The notice says what runs where for the hook resolved: the affected ones
// (measured from a full-scope anchor at pre-push, from HEAD at pre-commit)
// under select: affected, every workspace otherwise.
func TestResolve_WorkspacesNoticePerHook(t *testing.T) {
	for _, tc := range []struct {
		hook   domain.Hook
		mode   domain.WorkspaceSelectMode
		want   string
		forbid string
	}{
		{domain.PrePush, domain.WorkspaceSelectAffected, "trusted full-scope note", "staged change"},
		{domain.PrePush, domain.WorkspaceSelectAll, "every discovered workspace at pre-push", "staged change"},
		{domain.PreCommit, domain.WorkspaceSelectAffected, "staged change affects", "at pre-push"},
		{domain.PreCommit, domain.WorkspaceSelectAll, "every discovered workspace (select: all)", "staged change"},
	} {
		cfg := domain.Config{
			Workspaces:   &domain.WorkspacesConfig{Discover: []string{"js"}, Select: tc.mode},
			PerWorkspace: map[string]domain.PerWorkspaceStep{"test": {Each: "pnpm test"}},
		}
		ws := Resolve(cfg, Input{Hook: tc.hook}).Workspaces
		if !ws.Active || !strings.Contains(ws.Notice, tc.want) || strings.Contains(ws.Notice, tc.forbid) {
			t.Errorf("%s/%s: active=%v notice %q, want it to contain %q and not %q", tc.hook, tc.mode, ws.Active, ws.Notice, tc.want, tc.forbid)
		}
	}
}

// The configured globs reach Select unchanged: ignore exempts an unowned path
// that would otherwise force a full selection, and global forces one.
func TestResolve_WorkspacesFeedSelect(t *testing.T) {
	cfg := domain.Config{Workspaces: &domain.WorkspacesConfig{
		Discover: []string{"js"},
		Select:   domain.WorkspaceSelectAffected,
		Global:   []string{"tools/build/**"},
		Ignore:   []string{"docs/**"},
	}}
	res := Resolve(cfg, Input{Hook: domain.PrePush})
	ws := res.Workspaces
	if ws == nil || ws.Select != domain.WorkspaceSelectAffected {
		t.Fatalf("resolved workspaces = %+v", ws)
	}
	graph := domain.WorkspaceGraph{Workspaces: []domain.Workspace{
		{Path: "packages/ui", Name: "ui", Ecosystem: "js"},
		{Path: "apps/web", Name: "web", Ecosystem: "js", Deps: []string{"packages/ui"}},
		{Path: "apps/admin", Name: "admin", Ecosystem: "js"},
	}}
	sel := func(changed ...string) domain.Selection {
		t.Helper()
		s, err := domain.Select(ws.SelectionInput(graph, graph, changed, res.ConfigurationDigest, res.ConfigurationDigest))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	s := sel("packages/ui/button.ts", "docs/guide.md")
	if s.Kind != domain.SelectionAffected || !reflect.DeepEqual(s.Paths(), []string{"apps/web", "packages/ui"}) {
		t.Errorf("an ignored doc must not force full: kind=%s paths=%v full=%+v", s.Kind, s.Paths(), s.Full)
	}
	if !reflect.DeepEqual(s.Rules.Ignore, []string{"docs/**"}) || s.Rules.Unowned != domain.UnownedFull {
		t.Errorf("the selection must record the configured rules: %+v", s.Rules)
	}

	s = sel("packages/ui/button.ts", "tools/build/rollup.js")
	if s.Kind != domain.SelectionFull || s.Full.Rule != domain.FullRuleGlobal || s.Full.Pattern != "tools/build/**" {
		t.Errorf("a configured global path must force full: %+v", s.Full)
	}

	s = sel("ci/pipeline.yml")
	if s.Kind != domain.SelectionFull || s.Full.Rule != domain.FullRuleUnowned {
		t.Errorf("an unowned path outside ignore must force full by default: %+v", s.Full)
	}

	// unowned: ignore reaches Select as well.
	cfg.Workspaces.Unowned = domain.UnownedIgnore
	ws = Resolve(cfg, Input{Hook: domain.PrePush}).Workspaces
	if s := sel("ci/pipeline.yml"); s.Kind != domain.SelectionAffected || len(s.Workspaces) != 0 {
		t.Errorf("unowned: ignore must select nothing for an unowned path: %+v", s)
	}
}
