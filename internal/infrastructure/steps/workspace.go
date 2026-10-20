package steps

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// WorkspacePool bounds how many per-workspace commands run at once across
// every per-workspace step of a run. One pool is shared by the whole run, so
// a parallel batch of fan-out steps never multiplies the machine's load:
// three steps over twenty workspaces still run at most the pool's size of
// commands, not sixty (#249).
type WorkspacePool struct {
	slots chan struct{}
}

// NewWorkspacePool returns a pool of size n, or of DefaultWorkspaceConcurrency
// when n is not positive.
func NewWorkspacePool(n int) *WorkspacePool {
	if n <= 0 {
		n = DefaultWorkspaceConcurrency()
	}
	return &WorkspacePool{slots: make(chan struct{}, n)}
}

// DefaultWorkspaceConcurrency is the pool size when workspaces.concurrency
// is unset: half the CPUs, at least one. Per-workspace commands are usually
// whole test or lint runs that parallelize internally, and a parallel batch
// runs other steps beside them.
func DefaultWorkspaceConcurrency() int {
	return max(1, runtime.NumCPU()/2)
}

// Size is the number of commands the pool runs at once.
func (p *WorkspacePool) Size() int { return cap(p.slots) }

func (p *WorkspacePool) acquire(ctx context.Context) error {
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *WorkspacePool) release() { <-p.slots }

// WorkspaceStep is a step defined under per_workspace:. It is one step — one
// StepName, one result — that fans out internally: an `each:` command runs
// once per selected workspace, in that workspace's directory; a `batch:`
// command runs once, at the repository root, naming every selected workspace.
// Every command runs through the same shell path as a ShellStep (host or
// container, contention retries, failure interpretation), and the results fold
// into one StepResult with one WorkspaceOutcome per workspace.
type WorkspaceStep struct {
	name domain.StepName
	def  domain.PerWorkspaceStep
	pool *WorkspacePool
	// sequential runs one workspace at a time: set for a step whose writes
	// are kept (writes:, auto_fix), which runs as a barrier in the canonical
	// worktree, where concurrent writers would race each other.
	sequential bool
}

// NewWorkspaceStep binds a per-workspace definition to the run's shared pool.
func NewWorkspaceStep(name domain.StepName, def domain.PerWorkspaceStep, pool *WorkspacePool, sequential bool) WorkspaceStep {
	return WorkspaceStep{name: name, def: def, pool: pool, sequential: sequential}
}

// Name implements application.Step.
func (s WorkspaceStep) Name() domain.StepName { return s.name }

// Run implements application.Step.
func (s WorkspaceStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	if sc.Selection == nil {
		return domain.StepResult{}, errors.New("per-workspace step has no workspace selection to run over")
	}
	sel, err := sc.Selection(s.name)
	if err != nil {
		return domain.StepResult{}, err
	}
	targets := sel.Targets(s.def.Ecosystems)
	if sc.OnOutput != nil {
		sc.OnOutput(fmt.Sprintf("warden: %s runs per workspace (%s, %s): %s", s.name, s.def.Mode(), sel.Describe(), orNothing(workspacePaths(targets))))
	}
	if len(targets) == 0 {
		reason := "no workspace selected"
		if len(s.def.Ecosystems) > 0 {
			reason += " in " + strings.Join(s.def.Ecosystems, ", ")
		}
		return domain.StepResult{
			Step: s.name, Status: domain.StepPass,
			Outcome: domain.StepSkipped, OutcomeReason: reason,
			Summary: fmt.Sprintf("%s: %s (%s), skipped", s.name, reason, sel.Describe()),
		}, nil
	}
	var runs []workspaceRun
	if s.def.Batch != "" {
		runs = []workspaceRun{s.runOne(ctx, sc, "", batchScript(s.def.Batch, targets))}
	} else {
		runs = s.runEach(ctx, sc, targets)
	}
	res := s.fold(sc, sel, targets, runs)
	return res, nil
}

// workspaceRun is one command's result: for each:, one workspace's.
type workspaceRun struct {
	res         domain.StepResult
	cleanupNote string
}

func (s WorkspaceStep) runEach(ctx context.Context, sc application.StepContext, targets []domain.Workspace) []workspaceRun {
	runs := make([]workspaceRun, len(targets))
	if s.sequential {
		for i, w := range targets {
			runs[i] = s.runOne(ctx, sc, w.Path, eachScript(s.def.Each, w, targets))
		}
		return runs
	}
	var wg sync.WaitGroup
	for i, w := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs[i] = s.runOne(ctx, sc, w.Path, eachScript(s.def.Each, w, targets))
		}()
	}
	wg.Wait()
	return runs
}

// runOne runs one script in the worktree, holding a pool slot while it runs.
// label prefixes its streamed output, so concurrent workspaces stay legible.
func (s WorkspaceStep) runOne(ctx context.Context, sc application.StepContext, label, script string) workspaceRun {
	shell := ShellStep{name: s.name, cmdKey: string(s.name)}
	if label != "" && sc.OnOutput != nil {
		parent := sc.OnOutput
		sc.OnOutput = func(line string) { parent("[" + label + "] " + line) }
	}
	if err := s.pool.acquire(ctx); err != nil {
		return workspaceRun{res: shell.resultFor(ctx, sc, nil, err, false)}
	}
	out, err, contended := shell.runIn(ctx, sc, sc.WorktreeDir, script)
	s.pool.release()
	note, err := splitCleanup(err)
	return workspaceRun{res: shell.resultFor(ctx, sc, out, err, contended), cleanupNote: note}
}

// fold combines the commands' results into the step's one result.
func (s WorkspaceStep) fold(sc application.StepContext, sel *domain.WorkspaceSelection, targets []domain.Workspace, runs []workspaceRun) domain.StepResult {
	res := domain.StepResult{Step: s.name, Status: domain.StepPass}
	mode := s.def.Mode()
	var notes []string
	failingBlockers := []domain.Blocker{}
	for i, w := range targets {
		status := runs[0].res.Status
		if mode == domain.WorkspaceModeEach {
			status = runs[i].res.Status
		}
		res.Workspaces = append(res.Workspaces, domain.WorkspaceOutcome{Workspace: w.Path, Name: w.Name, Status: status, Mode: mode})
	}
	for i := range runs {
		run := &runs[i]
		if run.cleanupNote != "" {
			notes = append(notes, run.cleanupNote)
		}
		if run.res.Status != domain.StepFail {
			continue
		}
		res.Status = domain.StepFail
		failingBlockers = append(failingBlockers, run.res.Blocker)
		header := fmt.Sprintf("batch over %d workspace(s): %s", len(targets), strings.Join(workspacePaths(targets), ", "))
		if mode == domain.WorkspaceModeEach {
			header = fmt.Sprintf("workspace %s (%s)", targets[i].Path, targets[i].Name)
		}
		for _, f := range run.res.Findings {
			f.Message = header + ":\n" + f.Message
			res.Findings = append(res.Findings, f)
		}
	}
	// The run fails for the change unless every failure was an obstacle that
	// kept a command from running; then it names the first obstacle.
	if len(failingBlockers) > 0 && !containsBlocker(failingBlockers, domain.BlockerNone) {
		res.Blocker = failingBlockers[0]
	}
	if sc.Container != nil {
		env := sc.Container.Environment
		res.Environment = &env
	}
	res.Summary = s.summary(sel, targets, res)
	if len(notes) > 0 {
		res.Summary += "; " + strings.Join(notes, "; ")
	}
	return res
}

func (s WorkspaceStep) summary(sel *domain.WorkspaceSelection, targets []domain.Workspace, res domain.StepResult) string {
	scope := fmt.Sprintf("%d workspace(s); %s", len(targets), sel.Describe())
	switch {
	case res.Status != domain.StepFail:
		return fmt.Sprintf("%s passed in %s", s.name, scope)
	case s.def.Batch != "":
		return fmt.Sprintf("%s failed (one batch over %s)", s.name, scope)
	default:
		failed := res.FailedWorkspaces()
		return fmt.Sprintf("%s failed in %d of %d workspace(s): %s", s.name, len(failed), len(targets), strings.Join(failed, ", "))
	}
}

func containsBlocker(bs []domain.Blocker, b domain.Blocker) bool {
	for _, x := range bs {
		if x == b {
			return true
		}
	}
	return false
}

// eachScript is the shell script that runs an each: command for workspace w:
// it exports the workspace variables, changes into the workspace and runs the
// command with its placeholders replaced. Every substituted value is
// single-quoted. Names are validated against a strict character class, but a
// workspace PATH may hold any character a directory name can (spaces, '$',
// quotes), and both come from manifests a pull request controls.
//
// The working directory is set by the script rather than by the process, so
// the same script runs unchanged on the host and in a container, where only
// the worktree root is mounted.
func eachScript(command string, w domain.Workspace, selected []domain.Workspace) string {
	body := strings.NewReplacer(
		domain.PlaceholderWorkspacePath, shellQuote(w.Path),
		domain.PlaceholderWorkspaceName, shellQuote(w.Name),
	).Replace(command)
	return "export WARDEN_WORKSPACE_PATH=" + shellQuote(w.Path) +
		" WARDEN_WORKSPACE_NAME=" + shellQuote(w.Name) +
		" WARDEN_SELECTED_WORKSPACES=" + shellQuote(strings.Join(workspacePaths(selected), "\n")) + "\n" +
		"cd -- " + shellQuote(w.Path) + " || exit 1\n" +
		body
}

// batchScript is the shell script that runs a batch: command once, at the
// repository root, with the plural placeholders replaced by quoted lists.
// {workspaces.filter} becomes one --filter=<name> argument per workspace, the
// form turbo and pnpm both accept.
func batchScript(command string, selected []domain.Workspace) string {
	paths := workspacePaths(selected)
	names := make([]string, 0, len(selected))
	filters := make([]string, 0, len(selected))
	quotedPaths := make([]string, 0, len(selected))
	for _, w := range selected {
		names = append(names, shellQuote(w.Name))
		filters = append(filters, shellQuote("--filter="+w.Name))
	}
	for _, p := range paths {
		quotedPaths = append(quotedPaths, shellQuote(p))
	}
	body := strings.NewReplacer(
		domain.PlaceholderWorkspacesPaths, strings.Join(quotedPaths, " "),
		domain.PlaceholderWorkspacesNames, strings.Join(names, " "),
		domain.PlaceholderWorkspacesFilter, strings.Join(filters, " "),
	).Replace(command)
	return "export WARDEN_SELECTED_WORKSPACES=" + shellQuote(strings.Join(paths, "\n")) + "\n" + body
}

func workspacePaths(ws []domain.Workspace) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Path)
	}
	return out
}

func orNothing(paths []string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	return strings.Join(paths, ", ")
}
