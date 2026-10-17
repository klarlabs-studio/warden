package cli

import (
	"context"
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
	runWatchChecks(ctx, stdout, dir, cmds) // run once up front

	last := watch.Fingerprint(dir)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprintln(stdout, "\nwarden: watch stopped.")
			return 0
		case <-ticker.C:
			if watch.Fingerprint(dir) != last {
				runWatchChecks(ctx, stdout, dir, cmds)
				// Re-fingerprint after the run so checks that write files (rare in
				// watch) don't retrigger themselves.
				last = watch.Fingerprint(dir)
			}
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
		svc.SetObserver(&predictionReporter{ctx: ctx, w: stdout})
		result, err := svc.PredictStaged(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if result.Outcome != domain.OutcomePassed {
			return fmt.Errorf("staged checks %s: %s", result.Outcome, result.Message)
		}
		_, _ = fmt.Fprintln(stdout, "warden: staged checks passed; saved predictions expire after 10 minutes")
		return nil
	}
	defer svc.SetObserver(nil)
	if once {
		if err := evaluate(ctx); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	fingerprint := func(ctx context.Context) (string, error) {
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
	}
	_, _ = fmt.Fprintln(stdout, "warden: watching staged inputs (ctrl+c to stop); checks run in disposable container worktrees")
	watch.RunStaged(ctx, pollInterval, fingerprint, evaluate, func(err error) {
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "warden:", err)
		}
	})
	_, _ = fmt.Fprintln(stdout, "warden: staged watch stopped")
	return 0
}

type predictionReporter struct {
	ctx context.Context
	w   io.Writer
	mu  sync.Mutex
}

func (r *predictionReporter) OnStep(event application.StepEvent) {
	if event.Phase != application.StepFinished || r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = fmt.Fprintf(r.w, "  %s: %s\n", event.Result.Status, event.Result.Summary)
	if event.Result.Status != domain.StepPass {
		printFindings(r.w, event.Result.Findings)
	}
}
