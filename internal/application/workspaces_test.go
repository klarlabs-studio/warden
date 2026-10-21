package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// fakeWorkspaces serves graphs by revision and records what it was asked.
type fakeWorkspaces struct {
	tree      string
	graphs    map[string]domain.WorkspaceGraph
	changed   []string
	digest    string
	digestErr error
	asked     []string
	// ecosystems records what each Discover call asked for.
	ecosystems []string
}

func (f *fakeWorkspaces) TrackedTree(dir string) (string, error) {
	f.asked = append(f.asked, "tree:"+dir)
	return f.tree, nil
}

func (f *fakeWorkspaces) Discover(rev string, ecosystems []string) (domain.WorkspaceGraph, error) {
	f.asked = append(f.asked, "discover:"+rev)
	f.ecosystems = append(f.ecosystems, strings.Join(ecosystems, ","))
	return f.graphs[rev], nil
}

func (f *fakeWorkspaces) ChangedPaths(from, to string) ([]string, error) {
	f.asked = append(f.asked, "changed:"+from+".."+to)
	return f.changed, nil
}

func (f *fakeWorkspaces) ConfigurationDigestAt(rev string) (string, error) {
	return f.digest, f.digestErr
}

// pnpm-shaped: ui <- web, api alone, plus a Go module outside discover.
func wsGraph() domain.WorkspaceGraph {
	return domain.WorkspaceGraph{Workspaces: []domain.Workspace{
		{Path: "packages/ui", Name: "@x/ui", Ecosystem: "js"},
		{Path: "apps/web", Name: "web", Ecosystem: "js", Deps: []string{"packages/ui"}},
		{Path: "apps/api", Name: "api", Ecosystem: "js"},
		{Path: "svc", Name: "example.com/svc", Ecosystem: "go"},
	}}
}

func wsConfig(hookSteps map[string][]domain.StepName, mode domain.WorkspaceSelectMode) domain.Config {
	return domain.Config{
		Hooks:        domain.HookConfig{PreCommit: true, PrePush: true},
		Steps:        hookSteps,
		Commands:     map[string]string{"lint": "true", "fmt": "gofmt -w ."},
		Workspaces:   &domain.WorkspacesConfig{Discover: []string{"js"}, Select: mode},
		PerWorkspace: map[string]domain.PerWorkspaceStep{"test": {Each: "pnpm test"}},
	}
}

// selectionRun runs the hook with a kernel that reads each step's selection
// the way a per-workspace step does, and returns them by step.
func selectionRun(t *testing.T, hook domain.Hook, cfg domain.Config, src *fakeWorkspaces) (map[domain.StepName][]string, map[domain.StepName]*domain.WorkspaceSelection, error) {
	t.Helper()
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "head1", wt: &fakeWorktree{dir: "/wt", headSHA: "head1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)
	r.Workspaces = src
	factory := &selectionFactory{fakeFactory: fakeFactory{kernel: kernel}}
	r.Kernels = factory
	got := map[domain.StepName][]string{}
	sels := map[domain.StepName]*domain.WorkspaceSelection{}
	kernel.during = func(step domain.StepName) {
		sel, err := factory.sc.Selection(step)
		if err != nil {
			return // not a per-workspace step
		}
		sels[step] = sel
		got[step] = sel.Selection.Paths()
	}
	_, err := r.Run(context.Background(), hook)
	return got, sels, err
}

type selectionFactory struct {
	fakeFactory
	sc StepContext
}

func (f *selectionFactory) New(p domain.ResolvedPolicy, sc StepContext, priors *[]domain.Finding, push PushFunc) (Kernel, error) {
	f.sc = sc
	return f.fakeFactory.New(p, sc, priors, push)
}

// Pre-commit under select: affected selects through domain.Select: the graph
// at HEAD against the graph of the worktree's tracked tree, over the paths
// between them, with the configuration digests compared.
func TestRunner_PreCommitAffectedSelection(t *testing.T) {
	cfg := wsConfig(map[string][]domain.StepName{"pre_commit": {"lint", "test"}}, domain.WorkspaceSelectAffected)
	digest, err := domain.DigestJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeWorkspaces{
		tree:    "tree1",
		graphs:  map[string]domain.WorkspaceGraph{"head1": wsGraph(), "tree1": wsGraph()},
		changed: []string{"packages/ui/button.ts"},
		digest:  digest,
	}
	got, sels, err := selectionRun(t, domain.PreCommit, cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"apps/web", "packages/ui"}; !reflect.DeepEqual(got["test"], want) {
		t.Fatalf("selected %v, want %v (asked %v)", got["test"], want, src.asked)
	}
	if sels["test"].Selection.Kind != domain.SelectionAffected {
		t.Fatalf("kind %s", sels["test"].Selection.Kind)
	}
	// discover: [js] keeps the Go module out of the graph the step sees.
	if len(sels["test"].Graph.Workspaces) != 3 {
		t.Fatalf("graph %+v", sels["test"].Graph)
	}
	if _, ok := got["lint"]; ok {
		t.Fatal("a repo-wide step must get no selection")
	}
	if !strings.Contains(strings.Join(src.asked, " "), "changed:head1..tree1") {
		t.Fatalf("the change must be measured from HEAD to the checked tree: %v", src.asked)
	}
	// Both graphs are discovered for workspaces.discover only, so a manifest
	// of another ecosystem is never read.
	if len(src.ecosystems) != 2 || src.ecosystems[0] != "js" || src.ecosystems[1] != "js" {
		t.Fatalf("discovered ecosystems %q, want js for both graphs", src.ecosystems)
	}

	// A configuration that differs from HEAD's (or cannot be read there)
	// forces full.
	src.digest, src.digestErr = "", errors.New("unreadable")
	got, sels, err = selectionRun(t, domain.PreCommit, cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["test"]) != 3 || sels["test"].Selection.Full.Rule != domain.FullRuleConfiguration {
		t.Fatalf("an unknown configuration at HEAD must force full: %v %+v", got["test"], sels["test"].Selection.Full)
	}
}

func TestRunner_WorkspaceSelectionForcedFull(t *testing.T) {
	src := func() *fakeWorkspaces {
		return &fakeWorkspaces{tree: "tree1", graphs: map[string]domain.WorkspaceGraph{"head1": wsGraph(), "tree1": wsGraph()}, changed: []string{"packages/ui/a.ts"}}
	}
	affectedCommit := wsConfig(map[string][]domain.StepName{"pre_commit": {"test"}}, domain.WorkspaceSelectAffected)
	writerAfter := wsConfig(map[string][]domain.StepName{"pre_commit": {"test", "fmt"}}, domain.WorkspaceSelectAffected)
	writerAfter.Writes = []string{"fmt"}
	perWorkspaceWriter := wsConfig(map[string][]domain.StepName{"pre_commit": {"test"}}, domain.WorkspaceSelectAffected)
	perWorkspaceWriter.Writes = []string{"test"}
	writerBefore := wsConfig(map[string][]domain.StepName{"pre_commit": {"fmt", "test"}}, domain.WorkspaceSelectAffected)
	writerBefore.Writes = []string{"fmt"}
	for _, tc := range []struct {
		name string
		hook domain.Hook
		cfg  domain.Config
		rule domain.FullRule // "" = affected
	}{
		{"pre-push without a signer", domain.PrePush, wsConfig(map[string][]domain.StepName{"pre_push": {"test"}}, domain.WorkspaceSelectAffected), domain.FullRuleNoSigner},
		{"pre-push under select: all", domain.PrePush, wsConfig(map[string][]domain.StepName{"pre_push": {"test"}}, ""), domain.FullRuleSelectAll},
		{"select: all", domain.PreCommit, wsConfig(map[string][]domain.StepName{"pre_commit": {"test"}}, ""), domain.FullRuleSelectAll},
		{"a kept writer after the per-workspace step", domain.PreCommit, writerAfter, domain.FullRuleWriterOrder},
		{"a per-workspace step that keeps its own writes", domain.PreCommit, perWorkspaceWriter, domain.FullRuleWriterOrder},
		{"a kept writer before it leaves affected selection on", domain.PreCommit, writerBefore, ""},
		{"affected", domain.PreCommit, affectedCommit, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := src()
			s.digest, _ = domain.DigestJSON(tc.cfg)
			got, sels, err := selectionRun(t, tc.hook, tc.cfg, s)
			if err != nil {
				t.Fatal(err)
			}
			sel := sels["test"]
			if sel == nil {
				t.Fatalf("no selection for test (asked %v)", s.asked)
			}
			if tc.rule == "" {
				if sel.Selection.Kind != domain.SelectionAffected || len(got["test"]) != 2 {
					t.Fatalf("want affected over ui+web, got %s %v", sel.Selection.Kind, got["test"])
				}
				return
			}
			if sel.Selection.Kind != domain.SelectionFull || sel.Selection.Full.Rule != tc.rule || len(got["test"]) != 3 {
				t.Fatalf("want full (%s) over all 3 js workspaces, got %s %+v %v", tc.rule, sel.Selection.Kind, sel.Selection.Full, got["test"])
			}
		})
	}
}

// Without a workspace source a per-workspace step cannot know what to run
// over; the run fails instead of passing it over nothing.
func TestRunner_PerWorkspaceStepWithoutSourceFails(t *testing.T) {
	cfg := wsConfig(map[string][]domain.StepName{"pre_commit": {"test"}}, domain.WorkspaceSelectAffected)
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "head1", wt: &fakeWorktree{dir: "/wt", headSHA: "head1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)
	if _, err := r.Run(context.Background(), domain.PreCommit); err == nil || !strings.Contains(err.Error(), "workspace discovery") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if len(kernel.ran) != 0 {
		t.Fatalf("nothing should run: %v", kernel.ran)
	}

	// A run with no per-workspace step needs no source at all.
	cfg = wsConfig(map[string][]domain.StepName{"pre_commit": {"lint"}}, domain.WorkspaceSelectAffected)
	r = newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, cfg)
	if _, err := r.Run(context.Background(), domain.PreCommit); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceSelections_UnknownStep(t *testing.T) {
	if _, err := newWorkspaceSelections().get("test"); err == nil {
		t.Fatal("a step with no computed selection must get an error")
	}
}
