package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/watch"
	"go.klarlabs.de/warden/internal/service"
)

// pollInterval is how often watch samples the tree. Fast enough to feel live,
// slow enough to coalesce a burst of editor saves.
const pollInterval = 400 * time.Millisecond

// cmdWatch supports live-file feedback and an explicit staged mode that warms
// exact local predictions in disposable container worktrees. Neither mode pushes.
func cmdWatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	staged := fs.Bool("staged", false, "precompute opted-in checks against the Git staging area")
	once := fs.Bool("once", false, "with --staged, evaluate once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*once && !*staged) {
		_, _ = fmt.Fprintln(stderr, "warden: watch accepts --staged [--once]")
		return 2
	}

	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	defer disableOptionalLocks()()
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	cfg, err := svc.Config()
	if err != nil {
		return fail(stderr, err)
	}

	if *staged {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runStagedWatch(ctx, svc, dir, *once, stdout, stderr)
	}
	if cfg.Execution.ContainerEnabled() {
		return fail(stderr, fmt.Errorf("watch runs in the live tree and does not support container execution; use warden run pre-commit for isolated container checks"))
	}
	cmds := watchCommands(cfg)
	if len(cmds) == 0 {
		_, _ = fmt.Fprintln(stderr, "warden: no pre-commit commands configured to watch; add commands + steps.pre_commit")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	_, _ = fmt.Fprintf(stdout, "warden: watching %s (ctrl+c to stop) — %d check(s)\n", dir, len(cmds))
	runLiveWatch(ctx, dir, cmds, stdout)
	_, _ = fmt.Fprintln(stdout, "\nwarden: watch stopped.")
	return 0
}

// runLiveWatch re-runs cmds in dir whenever the tree's content changes. It
// shares the staged watcher's loop: a snapshot is evaluated once two samples
// agree, and a change during a run cancels it and evaluates the new snapshot,
// so an edit saved mid-run is never absorbed unchecked.
func runLiveWatch(ctx context.Context, dir string, cmds []namedCommand, stdout io.Writer) {
	tree := watch.NewTree(dir)
	watch.Loop{
		Interval:    pollInterval,
		Fingerprint: func(context.Context) (string, error) { return tree.Fingerprint(), nil },
		Evaluate: func(run context.Context) error {
			runWatchChecks(run, stdout, dir, cmds)
			if run.Err() != nil && ctx.Err() == nil {
				_, _ = fmt.Fprintln(stdout, "  ↻ superseded by a newer change")
			}
			return nil
		},
	}.Run(ctx)
}

// disableOptionalLocks sets GIT_OPTIONAL_LOCKS=0 for every git process the
// watcher starts, including those the service's git adapter spawns, so a
// background evaluation never takes index.lock and fails the developer's own
// commit. It returns a function restoring the previous value.
func disableOptionalLocks() func() {
	key, value, _ := strings.Cut(watch.OptionalLocksOff, "=")
	previous, had := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if had {
			_ = os.Setenv(key, previous)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// namedCommand is a pre-commit step and the shell command it runs.
type namedCommand struct {
	step    domain.StepName
	command string
}

// watchCommands resolves the pre-commit steps to their shell commands, skipping
// steps with no command (agent steps and unconfigured ones don't fit a fast
// save-loop).
func watchCommands(cfg domain.Config) []namedCommand {
	steps := cfg.Steps["pre_commit"]
	if len(steps) == 0 {
		steps = domain.DefaultSteps(domain.PreCommit)
	}
	var out []namedCommand
	for _, s := range steps {
		if cmd := strings.TrimSpace(cfg.Commands[string(s)]); cmd != "" {
			out = append(out, namedCommand{step: s, command: cmd})
		}
	}
	return out
}

// runWatchChecks runs each command in dir and prints a pass/fail line with
// timing. On failure it prints the command's output so the developer sees why.
func runWatchChecks(ctx context.Context, w io.Writer, dir string, cmds []namedCommand) {
	_, _ = fmt.Fprintf(w, "\n── %s ──\n", time.Now().Format("15:04:05"))
	for _, c := range cmds {
		start := time.Now()
		cmd := exec.CommandContext(ctx, "sh", "-c", c.command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(start).Seconds()
		if err != nil {
			if ctx.Err() != nil {
				return // interrupted mid-check
			}
			_, _ = fmt.Fprintf(w, "  ✗ %s (%.1fs)\n", c.step, elapsed)
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				_, _ = fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(trimmed, "\n", "\n    "))
			}
			continue
		}
		_, _ = fmt.Fprintf(w, "  ✓ %s (%.1fs)\n", c.step, elapsed)
	}
}

// Bounds for retrying a staged evaluation the machine prevented. Variables so
// tests need not wait out the production backoff.
var (
	stagedRetryMin = watch.DefaultRetryMin
	stagedRetryMax = watch.DefaultRetryMax
)

// stagedBlocked is a staged evaluation that did not run because of the machine.
type stagedBlocked struct {
	kind domain.Blocker
	err  error
}

func (b stagedBlocked) Error() string { return b.err.Error() }
func (b stagedBlocked) Unwrap() error { return b.err }

func runStagedWatch(ctx context.Context, svc *service.Service, dir string, once bool, stdout, stderr io.Writer) int {
	cfg, err := svc.Config()
	if err != nil {
		return fail(stderr, err)
	}
	if !cfg.Execution.ContainerEnabled() || len(cfg.PredictiveCache) == 0 {
		return fail(stderr, fmt.Errorf("staged watch needs a container image and predictive_cache checks"))
	}
	public, _ := svc.SigningKey()
	if public == "" {
		return fail(stderr, fmt.Errorf("staged watch needs a local signing key"))
	}
	evaluate := func(ctx context.Context) error {
		return evaluateStaged(ctx, svc, dir, stdout)
	}
	defer svc.SetObserver(nil)
	if once {
		return reportStagedOnce(evaluate(ctx), stdout, stderr)
	}
	gate, err := stagedGate(ctx, svc, dir)
	if err != nil && ctx.Err() == nil {
		return fail(stderr, err)
	}
	if ctx.Err() != nil { // interrupted while starting: a stop, not a failure
		_, _ = fmt.Fprintln(stdout, "warden: staged watch stopped")
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "warden: watching staged inputs (ctrl+c to stop); checks run in disposable container worktrees")
	watch.Loop{
		Interval:    pollInterval,
		Fingerprint: gate.Fingerprint,
		Evaluate:    evaluate,
		Report:      func(e watch.Event) { reportStagedEvent(e, stdout, stderr) },
		RetryMin:    stagedRetryMin,
		RetryMax:    stagedRetryMax,
	}.Run(ctx)
	_, _ = fmt.Fprintln(stdout, "warden: staged watch stopped")
	return 0
}

// evaluateStaged runs the predictive staged checks once. Nothing staged is
// watch.ErrIdle; a run the machine prevented is wrapped with watch.Blocked so
// the loop retries it rather than recording it as the snapshot's verdict.
func evaluateStaged(ctx context.Context, svc *service.Service, dir string, stdout io.Writer) error {
	// An inspection failure is not "nothing staged": evaluate, and let the
	// run report what is wrong.
	if empty, err := watch.NothingStaged(ctx, dir); err == nil && empty {
		return watch.ErrIdle
	}
	reporter := &predictionReporter{ctx: ctx, w: stdout}
	svc.SetObserver(reporter)
	result, err := svc.PredictStaged(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if errors.Is(err, application.ErrContainerUnavailable) {
			return watch.Blocked(stagedBlocked{kind: domain.BlockerEnvironment, err: err})
		}
		return err
	}
	if kind := firstBlocker(result.Blocker, reporter.blocker()); kind != domain.BlockerNone {
		return watch.Blocked(stagedBlocked{kind: kind, err: fmt.Errorf("staged checks could not run: %s", result.Message)})
	}
	if result.Outcome != domain.OutcomePassed {
		return fmt.Errorf("staged checks %s: %s", result.Outcome, result.Message)
	}
	_, _ = fmt.Fprintln(stdout, "warden: staged checks passed; saved predictions expire after 10 minutes")
	return nil
}

func firstBlocker(blockers ...domain.Blocker) domain.Blocker {
	for _, b := range blockers {
		if b != domain.BlockerNone {
			return b
		}
	}
	return domain.BlockerNone
}

// stagedGate fingerprints the staged inputs plus the configuration, hashing
// the index and loading the configuration only when a stat of their files
// changes (or the backstop elapses).
func stagedGate(ctx context.Context, svc *service.Service, dir string) (*watch.StatGate, error) {
	key, err := watch.StagedStatKey(ctx, dir, config.FileName)
	if err != nil {
		return nil, err
	}
	return &watch.StatGate{
		Key: key,
		Full: func(ctx context.Context) (string, error) {
			index, err := watch.StagedFingerprint(ctx, dir)
			if err != nil {
				return "", err
			}
			cfg, err := svc.Config()
			if err != nil {
				return "", err
			}
			configDigest, err := domain.DigestJSON(cfg)
			if err != nil {
				return "", err
			}
			return index + configDigest, nil
		},
	}, nil
}

func reportStagedOnce(err error, stdout, stderr io.Writer) int {
	var blocked stagedBlocked
	switch {
	case err == nil:
		return 0
	case errors.Is(err, watch.ErrIdle):
		_, _ = fmt.Fprintln(stdout, "warden: nothing staged; no checks run")
		return 0
	case errors.As(err, &blocked):
		_, _ = fmt.Fprintf(stderr, "warden: blocked: %v\n", err)
		return exitForBlocker(blocked.kind)
	default:
		return fail(stderr, err)
	}
}

func reportStagedEvent(e watch.Event, stdout, stderr io.Writer) {
	switch e.State {
	case watch.StateIdle:
		_, _ = fmt.Fprintln(stdout, "warden: nothing staged; no checks run")
	case watch.StateBlocked:
		_, _ = fmt.Fprintf(stderr, "warden: blocked: %v; retrying in %s\n", e.Err, e.RetryIn)
	default:
		if e.Err != nil {
			_, _ = fmt.Fprintln(stderr, "warden:", e.Err)
		}
	}
}

type predictionReporter struct {
	ctx      context.Context
	w        io.Writer
	mu       sync.Mutex
	blockers []domain.Blocker
}

// blocker returns the first environmental obstacle a finished step reported.
func (r *predictionReporter) blocker() domain.Blocker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return firstBlocker(r.blockers...)
}

func (r *predictionReporter) OnStep(event application.StepEvent) {
	if event.Phase != application.StepFinished || r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Result.Blocker != domain.BlockerNone {
		r.blockers = append(r.blockers, event.Result.Blocker)
	}
	_, _ = fmt.Fprintf(r.w, "  %s: %s\n", event.Result.Status, event.Result.Summary)
	if event.Result.Status != domain.StepPass {
		printFindings(r.w, event.Result.Findings)
	}
}
