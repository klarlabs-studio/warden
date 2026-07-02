package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/watch"
)

// pollInterval is how often watch samples the tree. Fast enough to feel live,
// slow enough to coalesce a burst of editor saves.
const pollInterval = 400 * time.Millisecond

// cmdWatch handles `warden watch`: a continuous feedback loop that re-runs the
// fast pre-commit step commands against the working tree whenever a file
// changes. Unlike the gate, it runs in the live tree (so it reflects unsaved
// edits) and never pushes — it is a dev-loop convenience, not the gate.
func cmdWatch(_ []string, stdout, stderr io.Writer) int {
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

	cmds := watchCommands(cfg)
	if len(cmds) == 0 {
		fmt.Fprintln(stderr, "warden: no pre-commit commands configured to watch; add commands + steps.pre_commit")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(stdout, "warden: watching %s (ctrl+c to stop) — %d check(s)\n", dir, len(cmds))
	runWatchChecks(ctx, stdout, dir, cmds) // run once up front

	last := watch.Fingerprint(dir)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "\nwarden: watch stopped.")
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
	fmt.Fprintf(w, "\n── %s ──\n", time.Now().Format("15:04:05"))
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
			fmt.Fprintf(w, "  ✗ %s (%.1fs)\n", c.step, elapsed)
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(trimmed, "\n", "\n    "))
			}
			continue
		}
		fmt.Fprintf(w, "  ✓ %s (%.1fs)\n", c.step, elapsed)
	}
}
