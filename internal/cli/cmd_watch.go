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
	"go.klarlabs.de/warden/internal/infrastructure/attach"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
	"go.klarlabs.de/warden/internal/infrastructure/watch"
	"go.klarlabs.de/warden/internal/service"
)

// pollInterval is how often watch samples the tree. Fast enough to feel live,
// slow enough to coalesce a burst of editor saves.
const pollInterval = 400 * time.Millisecond

// cmdWatch supports live-file feedback and an explicit staged mode that warms
// exact local predictions in disposable container worktrees. Under a container
// configuration live mode checks a working-tree snapshot in a disposable
// container worktree too. No mode pushes.
func cmdWatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	staged := fs.Bool("staged", false, "precompute opted-in checks against the Git staging area")
	once := fs.Bool("once", false, "evaluate once and exit (staged mode, or live mode under container execution)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "warden: watch accepts [--staged] [--once]")
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
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runLiveContainerWatch(ctx, svc, dir, *once, stdout, stderr)
	}
	if *once {
		_, _ = fmt.Fprintln(stderr, "warden: watch --once needs --staged, or container execution for live mode")
		return 2
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
	names := cfg.Steps["pre_commit"]
	if len(names) == 0 {
		names = domain.DefaultSteps(domain.PreCommit)
	}
	var out []namedCommand
	for _, s := range names {
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
	fb, code := startFeedback(ctx, svc, dir, domain.FeedbackModeStaged, stderr)
	if fb == nil {
		return code
	}
	ctx = fb.ctx
	defer fb.close()
	evaluate := func(ctx context.Context) error {
		return evaluateStaged(ctx, svc, dir, stdout, fb)
	}
	defer svc.SetObserver(nil)
	if once {
		fb.observeEnvironment(sampleEnvironmentOnce(ctx, cfg.Execution))
		code := reportStagedOnce(evaluate(ctx), stdout, stderr)
		return fb.exitCode(code, stderr)
	}
	snapshot, err := newStagedSnapshot(ctx, svc, dir, stdout)
	return watchSnapshots(ctx, svc, fb, snapshot, err, evaluate, "staged", stdout, stderr)
}

// watchSnapshots runs the coalescing loop over a snapshot fingerprint, the
// part staged and live container watching share. what names the mode in the
// start and stop lines.
func watchSnapshots(ctx context.Context, svc *service.Service, fb *feedbackSession, snapshot *stagedSnapshot, snapshotErr error, evaluate func(context.Context) error, what string, stdout, stderr io.Writer) int {
	if snapshotErr != nil && ctx.Err() == nil {
		return fail(stderr, snapshotErr)
	}
	if ctx.Err() != nil { // interrupted while starting: a stop, not a failure
		_, _ = fmt.Fprintf(stdout, "warden: %s watch stopped\n", what)
		return fb.exitCode(0, stderr)
	}
	snapshot.onEnvironment = fb.observeEnvironment
	inputs := "staged inputs"
	if snapshot.tree != nil {
		inputs = "the working tree"
	}
	_, _ = fmt.Fprintf(stdout, "warden: watching %s (ctrl+c to stop); checks run in disposable container worktrees\n", inputs)
	watch.Loop{
		Interval:    pollInterval,
		Fingerprint: snapshot.Fingerprint,
		Evaluate:    evaluate,
		Report: func(e watch.Event) {
			fb.observeLoop(e)
			reportStagedEvent(e, stdout, stderr)
		},
		Hold:     gateRunning(svc),
		RetryMin: stagedRetryMin,
		RetryMax: stagedRetryMax,
	}.Run(ctx)
	_, _ = fmt.Fprintf(stdout, "warden: %s watch stopped\n", what)
	return fb.exitCode(0, stderr)
}

// runLiveContainerWatch is live mode under a container configuration. Each
// evaluation checks a working-tree snapshot (the tree `git add -A` would
// stage, computed from a private index) in a disposable dependency-free
// container worktree. Only container shell checks run; nothing is predicted,
// fixed, signed or noted, and the live index is never written.
func runLiveContainerWatch(ctx context.Context, svc *service.Service, dir string, once bool, stdout, stderr io.Writer) int {
	resolved, err := svc.Explain(domain.PreCommit, "", nil)
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := application.LiveChecks(resolved); err != nil {
		return fail(stderr, err)
	}
	cfg, err := svc.Config()
	if err != nil {
		return fail(stderr, err)
	}
	fb, code := startFeedback(ctx, svc, dir, domain.FeedbackModeLive, stderr)
	if fb == nil {
		return code
	}
	ctx = fb.ctx
	defer fb.close()
	live := &liveEvaluator{svc: svc, dir: dir, stdout: stdout, fb: fb}
	defer svc.SetObserver(nil)
	if once {
		fb.observeEnvironment(sampleEnvironmentOnce(ctx, cfg.Execution))
		code := reportStagedOnce(live.evaluate(ctx), stdout, stderr)
		return fb.exitCode(code, stderr)
	}
	snapshot, err := newStagedSnapshot(ctx, svc, dir, stdout)
	if err == nil {
		// The working tree's content joins the staged inputs: an edit, a new
		// file or a deletion is a change; so is a commit (HEAD) or a
		// configuration change.
		snapshot.tree = watch.NewTree(fb.worktree)
	}
	return watchSnapshots(ctx, svc, fb, snapshot, err, live.evaluate, "live", stdout, stderr)
}

// liveEvaluator evaluates working-tree snapshots for the live container
// watcher.
type liveEvaluator struct {
	svc    *service.Service
	dir    string
	stdout io.Writer
	fb     *feedbackSession
	// last is the result of the evaluation the document describes.
	last error
}

// evaluate computes the working-tree snapshot and checks it. The loop calls
// it whenever its fingerprint changes, which a change the snapshot does not
// include also does (an ignored file, a `git add`). When the snapshot is the
// one the document already describes, under the same environment, the
// previous result stands and nothing runs.
func (l *liveEvaluator) evaluate(ctx context.Context) error {
	snapshot, expected, err := l.fb.liveSnapshot(ctx, l.svc, l.dir)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("working-tree snapshot: %w", err)
	}
	if l.fb.describes(snapshot) {
		return l.last
	}
	l.fb.beginSnapshot(ctx, snapshot, expected)
	reporter := &predictionReporter{ctx: ctx, w: l.stdout}
	l.svc.SetObserver(application.MultiObserver{reporter, l.fb.observer()})
	result, err := l.svc.CheckWorkingTree(ctx, snapshot.Base, snapshot.Tree)
	err = classifyEvaluation(ctx, result, err, reporter, "live")
	if err == nil {
		_, _ = fmt.Fprintln(l.stdout, "warden: live checks passed for the working tree")
	}
	l.fb.record(ctx, err)
	if ctx.Err() == nil && !watch.IsBlocked(err) {
		l.last = err
	}
	return err
}

// evaluateStaged runs the predictive staged checks once. Nothing staged is
// watch.ErrIdle; a run the machine prevented is wrapped with watch.Blocked so
// the loop retries it rather than recording it as the snapshot's verdict.
func evaluateStaged(ctx context.Context, svc *service.Service, dir string, stdout io.Writer, fb *feedbackSession) error {
	if err := fb.begin(ctx, svc, dir); err != nil {
		return err
	}
	err := runStagedEvaluation(ctx, svc, dir, stdout, fb)
	fb.record(ctx, err)
	return err
}

func runStagedEvaluation(ctx context.Context, svc *service.Service, dir string, stdout io.Writer, fb *feedbackSession) error {
	// An inspection failure is not "nothing staged": evaluate, and let the
	// run report what is wrong.
	if empty, err := watch.NothingStaged(ctx, dir); err == nil && empty {
		return watch.ErrIdle
	}
	reporter := &predictionReporter{ctx: ctx, w: stdout}
	svc.SetObserver(application.MultiObserver{reporter, fb.observer()})
	result, err := svc.PredictStaged(ctx)
	if err := classifyEvaluation(ctx, result, err, reporter, "staged"); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "warden: staged checks passed; saved predictions expire after 10 minutes")
	return nil
}

// classifyEvaluation turns a background run into the loop's terms: nil for a
// pass, watch.Blocked for a run the machine prevented (retried, never the
// snapshot's verdict), and an error for a failure. what names the mode.
func classifyEvaluation(ctx context.Context, result application.RunResult, err error, reporter *predictionReporter, what string) error {
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
		return watch.Blocked(stagedBlocked{kind: kind, err: fmt.Errorf("%s checks could not run: %s", what, result.Message)})
	}
	if result.Outcome != domain.OutcomePassed {
		return fmt.Errorf("%s checks %s: %s", what, result.Outcome, result.Message)
	}
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

// Bounds for sampling the container environment. Variables so tests need not
// wait out the production TTL.
var (
	// environmentSampleTTL: see watch.DefaultSampleTTL for why 15 s.
	environmentSampleTTL = watch.DefaultSampleTTL
	// environmentSampleTimeout bounds one inspection. The sample runs on the
	// watcher's polling goroutine, so it is shorter than the 10 s an
	// evaluation's own resolution allows; a slower answer is inconclusive and
	// keeps the previous observation.
	environmentSampleTimeout = 5 * time.Second
)

// environmentUnavailable is the sampled environment when no runtime's local
// store answered with a usable image: the engine is stopped, or the image is
// missing. It is a value of its own so that the engine coming back (or the
// image being built) is itself a change that starts an evaluation.
const environmentUnavailable = "unavailable"

// stagedSnapshot identifies what a staged evaluation would check: the staged
// inputs, the configuration, and the container environment the checks would
// run in. The environment is part of the snapshot because a prediction is
// bound to the image ID it ran under. Rebuilding or retagging the configured
// image, or switching runtimes, moves that ID, so a pass for the previous
// environment no longer describes what a commit would run, and the watcher
// must evaluate again rather than leave the old pass on screen.
type stagedSnapshot struct {
	gate *watch.StatGate
	// tree, when set, adds the working tree's content (live mode).
	tree *watch.Tree
	env  *watch.Sampler
	// execution is the configuration's execution block as of the last full
	// fingerprint; the sampler is keyed on it.
	execution domain.ExecutionConfig
	shown     string
	out       io.Writer
	// onEnvironment, when set, receives every sampled environment.
	onEnvironment func(string)
}

// newStagedSnapshot hashes the index and loads the configuration only when a
// stat of their files changes (or the backstop elapses), and inspects the
// environment at most once per environmentSampleTTL.
func newStagedSnapshot(ctx context.Context, svc *service.Service, dir string, out io.Writer) (*stagedSnapshot, error) {
	key, err := watch.StagedStatKey(ctx, dir, config.FileName)
	if err != nil {
		return nil, err
	}
	s := &stagedSnapshot{out: out}
	s.gate = &watch.StatGate{
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
			s.execution = cfg.Execution
			return index + configDigest, nil
		},
	}
	s.env = &watch.Sampler{
		TTL:    environmentSampleTTL,
		Sample: func(ctx context.Context) (string, error) { return sampleEnvironment(ctx, s.execution) },
	}
	return s, nil
}

// Fingerprint is the loop's snapshot identity. It also reports the sampled
// environment whenever it changes, so the developer can see which runtime,
// image and platform background checks are running against.
func (s *stagedSnapshot) Fingerprint(ctx context.Context) (string, error) {
	inputs, err := s.gate.Fingerprint(ctx)
	if err != nil {
		return "", err
	}
	if s.tree != nil {
		inputs = s.tree.Fingerprint() + "\x00" + inputs
	}
	key, err := domain.DigestJSON(s.execution)
	if err != nil {
		return "", err
	}
	env := s.env.Value(ctx, key)
	if s.onEnvironment != nil {
		s.onEnvironment(env)
	}
	if env != s.shown && env != "" {
		switch {
		case env == environmentUnavailable:
			_, _ = fmt.Fprintln(s.out, "warden: environment: no container engine has execution.image available")
		case s.shown == "" || s.shown == environmentUnavailable:
			_, _ = fmt.Fprintln(s.out, "warden: environment:", env)
		default:
			_, _ = fmt.Fprintln(s.out, "warden: environment changed:", env)
		}
		s.shown = env
	}
	return inputs + "\x00environment:" + env, nil
}

// sampleEnvironment inspects the configured image without resolving a
// runtime's engine state, pulling, or running anything.
func sampleEnvironment(ctx context.Context, execution domain.ExecutionConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, environmentSampleTimeout)
	defer cancel()
	env, err := steps.SampleEnvironment(ctx, execution)
	if err != nil {
		if ctx.Err() != nil {
			return "", err // timed out or stopping: inconclusive
		}
		return environmentUnavailable, nil
	}
	return describeEnvironment(env), nil
}

// sampleEnvironmentOnce samples the environment for a single evaluation. An
// inconclusive sample is recorded as no observation.
func sampleEnvironmentOnce(ctx context.Context, execution domain.ExecutionConfig) string {
	env, err := sampleEnvironment(ctx, execution)
	if err != nil {
		return ""
	}
	return env
}

// describeEnvironment names the runtime, image ID and platform checks run in.
func describeEnvironment(env domain.ExecutionEnvironment) string {
	if env.Runtime == "" {
		return "host"
	}
	text := fmt.Sprintf("runtime %s, image %s (%s), platform %s", env.Runtime, env.Image, env.ImageID, env.Platform)
	if env.ImageIndexDigest != "" {
		text += ", index " + env.ImageIndexDigest
	}
	return text
}

// gateRunning is the staged watcher's yield probe: a gate run in this
// worktree publishes on the attach socket, and while it does the watcher
// stops competing with it for the container engine. The hook never waits on
// the watcher, so this removes contention, not a deadlock. Without a git dir
// there is nothing to probe.
func gateRunning(svc *service.Service) func(context.Context) bool {
	gitDir, err := svc.GitDir()
	if err != nil {
		return nil
	}
	return func(context.Context) bool { return attach.Live(gitDir) }
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
	case watch.StateYielded:
		_, _ = fmt.Fprintln(stdout, "warden: a gate run is in progress; background checks paused")
	case watch.StateResumed:
		_, _ = fmt.Fprintln(stdout, "warden: gate run finished; background checks resumed")
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
	status := string(event.Result.Status)
	if n, ok := event.Result.Note(); ok {
		status += " (" + string(n.Outcome) + ")"
	}
	_, _ = fmt.Fprintf(r.w, "  %s: %s\n", status, event.Result.Summary)
	if env := event.Result.Environment; env != nil {
		_, _ = fmt.Fprintf(r.w, "    in %s\n", describeEnvironment(*env))
	}
	if event.Result.Status != domain.StepPass {
		printFindings(r.w, event.Result.Findings)
	}
}
