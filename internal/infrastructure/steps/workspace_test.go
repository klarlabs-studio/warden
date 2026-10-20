package steps

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// workspaceFixture makes a worktree directory holding the given workspaces'
// directories and returns a full selection over them.
func workspaceFixture(t *testing.T, ws ...domain.Workspace) (string, *domain.WorkspaceSelection) {
	t.Helper()
	g := domain.WorkspaceGraph{Workspaces: ws}
	if err := g.Validate(); err != nil {
		t.Fatalf("fixture graph is not one discovery could produce: %v", err)
	}
	dir := t.TempDir()
	for _, w := range ws {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(w.Path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sel, err := domain.FullSelection(g, domain.ForcedFull{Rule: domain.FullRuleSelectAll})
	if err != nil {
		t.Fatal(err)
	}
	return dir, &domain.WorkspaceSelection{Selection: sel, Graph: g.Canonical()}
}

func selectionOf(sel *domain.WorkspaceSelection) func(domain.StepName) (*domain.WorkspaceSelection, error) {
	return func(domain.StepName) (*domain.WorkspaceSelection, error) { return sel, nil }
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// A workspace path may hold any character a directory name can, and both the
// path and the name come from manifests a pull request controls. Every
// substituted value must reach the command as exactly one literal word:
// nothing in it may expand, split, or end the command.
func TestWorkspaceStep_EachQuotesEverySubstitutedValue(t *testing.T) {
	hostile := domain.Workspace{Path: "apps/we b$(touch PWNED);`touch PWNED2`'x\"*", Name: "@s/a~b.c_d-e", Ecosystem: "js"}
	tilde := domain.Workspace{Path: "pkgs/~home", Name: "x/~root", Ecosystem: "js"}
	dir, sel := workspaceFixture(t, hostile, tilde)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LOG", log)

	step := NewWorkspaceStep("test", domain.PerWorkspaceStep{
		Each: `printf '%s|%s|%s|%s|%s\n' {workspace.path} {workspace.name} "$WARDEN_WORKSPACE_PATH" "$WARDEN_WORKSPACE_NAME" "$(pwd -P)" >> "$LOG"`,
	}, NewWorkspacePool(1), false)
	res, err := step.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status %s: %+v", res.Status, res.Findings)
	}
	for _, f := range []string{"PWNED", "PWNED2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("a substituted value was executed: %s exists", f)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "*", f))
		if len(matches) > 0 {
			t.Fatalf("a substituted value was executed: %v", matches)
		}
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	want := []string{
		strings.Join([]string{hostile.Path, hostile.Name, hostile.Path, hostile.Name, filepath.Join(realDir, filepath.FromSlash(hostile.Path))}, "|"),
		strings.Join([]string{tilde.Path, tilde.Name, tilde.Path, tilde.Name, filepath.Join(realDir, filepath.FromSlash(tilde.Path))}, "|"),
	}
	got := readLines(t, log)
	sort.Strings(got) // workspaces run concurrently; the order they log in is not fixed
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("log\n%q\nwant\n%q", got, want)
	}
	wantOutcomes := []domain.WorkspaceOutcome{
		{Workspace: hostile.Path, Name: hostile.Name, Status: domain.StepPass, Mode: domain.WorkspaceModeEach},
		{Workspace: tilde.Path, Name: tilde.Name, Status: domain.StepPass, Mode: domain.WorkspaceModeEach},
	}
	if !reflect.DeepEqual(res.Workspaces, wantOutcomes) {
		t.Fatalf("outcomes %+v\nwant %+v", res.Workspaces, wantOutcomes)
	}
}

func TestWorkspaceStep_BatchRunsOnceWithQuotedLists(t *testing.T) {
	a := domain.Workspace{Path: "apps/web app", Name: "web", Ecosystem: "js"}
	b := domain.Workspace{Path: "packages/ui", Name: "@acme/ui", Ecosystem: "js"}
	dir, sel := workspaceFixture(t, a, b)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LOG", log)

	step := NewWorkspaceStep("typecheck", domain.PerWorkspaceStep{
		Batch: `{ printf 'pwd=%s\n' "$(pwd -P)"; printf 'p=%s\n' {workspaces.paths}; printf 'n=%s\n' {workspaces.names}; printf 'f=%s\n' {workspaces.filter}; printf 'env=%s\n' "$WARDEN_SELECTED_WORKSPACES"; } >> "$LOG"`,
	}, NewWorkspacePool(4), false)
	res, err := step.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status %s: %+v", res.Status, res.Findings)
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	want := []string{
		"pwd=" + realDir,
		"p=apps/web app", "p=packages/ui",
		"n=web", "n=@acme/ui",
		"f=--filter=web", "f=--filter=@acme/ui",
		"env=apps/web app", "packages/ui",
	}
	if got := readLines(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("log\n%q\nwant\n%q", got, want)
	}
	for _, w := range res.Workspaces {
		if w.Mode != domain.WorkspaceModeBatch {
			t.Fatalf("a batch outcome must say it was a batch: %+v", w)
		}
	}
}

// A failing workspace fails the step and is named; the others still run, so
// the result covers every selected workspace rather than stopping at the
// first failure.
func TestWorkspaceStep_FailureNamesTheWorkspace(t *testing.T) {
	ws := []domain.Workspace{
		{Path: "a", Name: "a", Ecosystem: "js"},
		{Path: "b", Name: "b", Ecosystem: "js"},
		{Path: "c", Name: "c", Ecosystem: "js"},
	}
	dir, sel := workspaceFixture(t, ws...)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LOG", log)
	step := NewWorkspaceStep("test", domain.PerWorkspaceStep{
		Each: `echo {workspace.path} >> "$LOG"; if [ {workspace.name} = b ]; then echo "b is broken"; exit 3; fi`,
	}, NewWorkspacePool(2), false)
	res, err := step.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status %s, want fail", res.Status)
	}
	if got := res.FailedWorkspaces(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("failed workspaces %v, want [b]", got)
	}
	if len(res.Findings) != 1 || !strings.HasPrefix(res.Findings[0].Message, "workspace b (b):\n") || !strings.Contains(res.Findings[0].Message, "b is broken") {
		t.Fatalf("finding must name the workspace and keep its output: %+v", res.Findings)
	}
	if !strings.Contains(res.Summary, "failed in 1 of 3 workspace(s): b") {
		t.Fatalf("summary %q", res.Summary)
	}
	got := readLines(t, log)
	if len(got) != 3 {
		t.Fatalf("every workspace must run, ran %v", got)
	}
	if res.Blocker != domain.BlockerNone {
		t.Fatalf("a command that ran and failed is a verdict, not a blocker: %s", res.Blocker)
	}
	run := domain.NewRun("run_1", domain.PreCommit, domain.ResolvedPolicy{Steps: []domain.StepName{"test"}}, "main")
	if err := run.RecordStep(res); err != nil {
		t.Fatal(err)
	}
	if run.Message() != "step test failed in workspace b" {
		t.Fatalf("run message %q", run.Message())
	}
}

// One pool bounds every per-workspace command of a run: two fan-out steps
// running at once over four workspaces each never exceed the pool's size
// together (#249: parallelism is shared, never multiplied).
func TestWorkspaceStep_SharedPoolBoundsConcurrencyAcrossSteps(t *testing.T) {
	var ws []domain.Workspace
	for i := range 4 {
		ws = append(ws, domain.Workspace{Path: "w" + strconv.Itoa(i), Name: "w" + strconv.Itoa(i), Ecosystem: "go"})
	}
	dir, sel := workspaceFixture(t, ws...)
	running := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("RUNNING", running)
	t.Setenv("LOG", log)
	// Each command marks itself running, records how many are running, waits,
	// then unmarks. Appends of one short line are atomic.
	cmd := `m="$RUNNING/$$"; mkdir "$m"; ls "$RUNNING" | wc -l | tr -d ' ' >> "$LOG"; sleep 0.2; rmdir "$m"`
	pool := NewWorkspacePool(2)
	steps := []WorkspaceStep{
		NewWorkspaceStep("test", domain.PerWorkspaceStep{Each: cmd}, pool, false),
		NewWorkspaceStep("lint", domain.PerWorkspaceStep{Each: cmd}, pool, false),
	}
	var wg sync.WaitGroup
	for _, s := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)})
			if err != nil || res.Status != domain.StepPass {
				t.Errorf("%s: %v %+v", s.Name(), err, res)
			}
		}()
	}
	wg.Wait()
	counts := readLines(t, log)
	if len(counts) != 8 {
		t.Fatalf("want 8 commands, got %v", counts)
	}
	for _, c := range counts {
		if n, _ := strconv.Atoi(c); n > 2 || n < 1 {
			t.Fatalf("%s commands ran at once with a pool of 2: %v", c, counts)
		}
	}
}

// A step whose writes are kept runs its workspaces one at a time, whatever
// the pool allows: they share the canonical worktree.
func TestWorkspaceStep_WriterRunsOneWorkspaceAtATime(t *testing.T) {
	var ws []domain.Workspace
	for i := range 3 {
		ws = append(ws, domain.Workspace{Path: "w" + strconv.Itoa(i), Name: "w" + strconv.Itoa(i), Ecosystem: "go"})
	}
	dir, sel := workspaceFixture(t, ws...)
	running := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("RUNNING", running)
	t.Setenv("LOG", log)
	cmd := `m="$RUNNING/$$"; mkdir "$m"; ls "$RUNNING" | wc -l | tr -d ' ' >> "$LOG"; sleep 0.1; rmdir "$m"`
	step := NewWorkspaceStep("fmt", domain.PerWorkspaceStep{Each: cmd}, NewWorkspacePool(8), true)
	if res, err := step.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)}); err != nil || res.Status != domain.StepPass {
		t.Fatalf("%v %+v", err, res)
	}
	if got := readLines(t, log); !reflect.DeepEqual(got, []string{"1", "1", "1"}) {
		t.Fatalf("a writer ran workspaces concurrently: %v", got)
	}
}

func TestWorkspaceStep_EcosystemsAndEmptySelection(t *testing.T) {
	dir, sel := workspaceFixture(t,
		domain.Workspace{Path: "web", Name: "web", Ecosystem: "js"},
		domain.Workspace{Path: "svc", Name: "example.com/svc", Ecosystem: "go"},
	)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LOG", log)
	goOnly := NewWorkspaceStep("vet", domain.PerWorkspaceStep{Each: `echo {workspace.path} >> "$LOG"`, Ecosystems: []string{"go"}}, NewWorkspacePool(1), false)
	res, err := goOnly.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(sel)})
	if err != nil || res.Status != domain.StepPass {
		t.Fatalf("%v %+v", err, res)
	}
	if got := readLines(t, log); !reflect.DeepEqual(got, []string{"svc"}) {
		t.Fatalf("ecosystems: [go] ran %v", got)
	}

	// Nothing selected: the step passes without claiming it ran.
	empty := &domain.WorkspaceSelection{Graph: sel.Graph, Selection: domain.Selection{Kind: domain.SelectionAffected}}
	res, err = goOnly.Run(context.Background(), application.StepContext{WorktreeDir: dir, Selection: selectionOf(empty)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass || res.Outcome != domain.StepSkipped || len(res.Workspaces) != 0 {
		t.Fatalf("an empty selection must be a skipped pass with no outcomes: %+v", res)
	}
	if got := readLines(t, log); len(got) != 1 {
		t.Fatalf("nothing should have run: %v", got)
	}
}

func TestWorkspaceStep_WithoutSelectionFails(t *testing.T) {
	step := NewWorkspaceStep("test", domain.PerWorkspaceStep{Each: "true"}, NewWorkspacePool(1), false)
	if _, err := step.Run(context.Background(), application.StepContext{WorktreeDir: t.TempDir()}); err == nil {
		t.Fatal("a per-workspace step with no selection must fail, not pass over nothing")
	}
}

func TestWorkspacePool_Default(t *testing.T) {
	if got := NewWorkspacePool(0).Size(); got != DefaultWorkspaceConcurrency() || got < 1 {
		t.Fatalf("default pool size %d", got)
	}
	if got := NewWorkspacePool(3).Size(); got != 3 {
		t.Fatalf("pool size %d, want 3", got)
	}
}
