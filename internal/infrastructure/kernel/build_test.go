package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
)

func TestResolveStep_SecurityScanGetsTheScannerAwareStep(t *testing.T) {
	// The `security-scan` command step is the one custom step warden knows how
	// to interpret: it reads the scanner's report so it can gate on the findings
	// the change introduced rather than the repo's whole backlog. Falling back to
	// a plain ShellStep here would silently restore total-state gating.
	got, err := resolveStep(application.Registry{}, domain.StepSecurityScan,
		domain.ResolvedPolicy{Commands: map[string]string{"security-scan": "nox scan . -severity-threshold high"}}, nil)
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if _, ok := got.(steps.SecurityScanStep); !ok {
		t.Errorf("resolveStep returned %T, want a SecurityScanStep", got)
	}
}

func TestResolveStep_OtherCommandStepsStayPlainShell(t *testing.T) {
	got, err := resolveStep(application.Registry{}, "codegen", domain.ResolvedPolicy{Commands: map[string]string{"codegen": "make gen"}}, nil)
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if _, ok := got.(steps.ShellStep); !ok {
		t.Errorf("resolveStep returned %T, want a ShellStep", got)
	}
}

func TestResolveStep_RegistryStillWins(t *testing.T) {
	// A registered implementation (a test fake, a future built-in) must keep
	// precedence over the command ladder.
	fake := &fakeStep{name: domain.StepSecurityScan, status: domain.StepPass}
	got, err := resolveStep(application.Registry{domain.StepSecurityScan: fake}, domain.StepSecurityScan,
		domain.ResolvedPolicy{Commands: map[string]string{"security-scan": "nox scan ."}}, nil)
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if got != application.Step(fake) {
		t.Errorf("resolveStep returned %T, want the registered step", got)
	}
}

func perWorkspacePolicy(steps []domain.StepName, defs map[domain.StepName]domain.PerWorkspaceStep) domain.ResolvedPolicy {
	return domain.ResolvedPolicy{
		Hook:       domain.PreCommit,
		Steps:      steps,
		Commands:   map[string]string{},
		Cache:      map[domain.StepName][]string{"test": {"**/*"}},
		Workspaces: &domain.ResolvedWorkspaces{Active: true, Discover: []string{"js"}, Steps: defs},
	}
}

// A step defined under per_workspace: fans out even when it shares a
// built-in's name, and a custom step defined only there resolves instead of
// failing with "define commands.<step>".
func TestResolveStep_PerWorkspaceWins(t *testing.T) {
	p := perWorkspacePolicy([]domain.StepName{"test", "e2e"}, map[domain.StepName]domain.PerWorkspaceStep{
		"test": {Each: "pnpm test"},
		"e2e":  {Batch: "turbo run e2e {workspaces.filter}"},
	})
	for _, name := range []domain.StepName{"test", "e2e"} {
		got, err := resolveStep(steps.Default(), name, p, steps.NewWorkspacePool(1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := got.(steps.WorkspaceStep); !ok {
			t.Errorf("%s resolved to %T, want a WorkspaceStep", name, got)
		}
	}
	_, err := resolveStep(application.Registry{}, "missing", p, nil)
	if err == nil || !strings.Contains(err.Error(), "per_workspace.missing") {
		t.Errorf("an undefined custom step should name per_workspace: as a way to define it: %v", err)
	}
}

// Through the real kernel: the per-workspace step runs once per selected
// workspace, is never served from the step cache, and leaves one
// `<step>.workspace` record per workspace in the run's evidence chain.
func TestBuild_PerWorkspaceStepRunsUncachedAndRecordsEvidence(t *testing.T) {
	g := domain.WorkspaceGraph{Workspaces: []domain.Workspace{
		{Path: "a", Name: "a", Ecosystem: "js"},
		{Path: "b", Name: "b", Ecosystem: "js"},
	}}
	sel, err := domain.FullSelection(g, domain.ForcedFull{Rule: domain.FullRuleSelectAll})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, w := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, w), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := perWorkspacePolicy([]domain.StepName{"test"}, map[domain.StepName]domain.PerWorkspaceStep{"test": {Each: "true"}})
	sc := application.StepContext{
		Hook: domain.PreCommit, WorktreeDir: dir,
		Selection: func(domain.StepName) (*domain.WorkspaceSelection, error) {
			return &domain.WorkspaceSelection{Selection: sel, Graph: g}, nil
		},
	}
	cache := &fakeCache{fp: "fp", seen: true}
	var priors []domain.Finding
	k, err := NewFactory(steps.Default()).WithCache(cache).New(p, sc, &priors, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := k.Execute(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.ExecutionOutcome() != domain.StepExecuted || len(out.Result.Workspaces) != 2 {
		t.Fatalf("a per-workspace step must run, not be cached: %+v", out.Result)
	}
	_, entries, err := k.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"test.pass", "test.workspace", "test.workspace"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("evidence kinds %v, want %v", kinds, want)
	}
}

func TestEvidenceFor_WorkspaceRecords(t *testing.T) {
	recs := evidenceFor(domain.StepResult{Step: "typecheck", Status: domain.StepFail, Workspaces: []domain.WorkspaceOutcome{
		{Workspace: "a", Status: domain.StepFail, Mode: domain.WorkspaceModeBatch},
	}})
	if len(recs) != 2 || recs[1].Kind != "typecheck.workspace" {
		t.Fatalf("records %+v", recs)
	}
	want := map[string]any{"workspace": "a", "status": "fail", "mode": "batch"}
	if !reflect.DeepEqual(recs[1].Value, want) {
		t.Fatalf("value %+v, want %+v", recs[1].Value, want)
	}
}
