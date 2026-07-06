package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/attach"
	"go.klarlabs.de/warden/internal/infrastructure/notify"
	"go.klarlabs.de/warden/internal/tui"
)

// cmdRun handles `warden run <hook>`, the entry point the installed hook shims
// call. Its exit code drives git: a pre-commit pass exits 0 so the commit
// proceeds; a pre-push run ALWAYS exits non-zero — on success because Warden
// has already performed the push itself and git's own (now-stale) push must be
// stopped from racing it (§4.3 step 4), on failure because the push is blocked.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: warden run <pre-commit|pre-push>")
		return 2
	}
	hook, err := domain.ParseHook(args[0])
	if err != nil {
		return fail(stderr, err)
	}

	// Derive the run's context from the interrupt signals so a Ctrl-C or
	// SIGTERM cancels the pipeline and, critically, aborts the push gate before
	// it can auto-approve (see Runner.resolvePushGate).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A pre-push run on a real terminal gets the live TUI; the fast pre-commit
	// path and non-interactive streams (CI, agents) print inline (§4.4).
	if hook == domain.PrePush && isInteractive() {
		return runWithTUI(ctx, hook, stdout, stderr)
	}

	svc, err := newService(newTerminalApprover(os.Stdin, stdout))
	if err != nil {
		return fail(stderr, err)
	}

	// A non-interactive pre-push still publishes to the attach socket, so another
	// terminal can watch it with `warden attach`.
	var server *attach.Server
	if hook == domain.PrePush {
		if server = startAttach(svc); server != nil {
			svc.SetObserver(server)
			defer server.Close()
		}
	}

	res, err := svc.Run(ctx, hook)
	if err != nil {
		return fail(stderr, err)
	}
	server.PublishDone(res) // nil-safe; broadcasts the outcome to any watcher
	printFindings(stdout, res.Findings)

	switch hook {
	case domain.PreCommit:
		return runPreCommitExit(svc, res, stdout, stderr)
	default:
		return runPrePushExit(res, stdout)
	}
}

// startAttach opens the per-repo attach socket for a run, or returns nil when it
// can't (attach is best-effort and never fails a run).
func startAttach(svc interface{ GitDir() (string, error) }) *attach.Server {
	gitDir, err := svc.GitDir()
	if err != nil {
		return nil
	}
	return attach.NewServer(gitDir)
}

// isInteractive reports whether both stdin and stdout are a terminal, so the
// TUI has something to attach to.
func isInteractive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// runWithTUI drives a pre-push run under the live TUI.
func runWithTUI(ctx context.Context, hook domain.Hook, stdout, stderr io.Writer) int {
	br := tui.NewApprover()
	svc, err := newService(br)
	if err != nil {
		return fail(stderr, err)
	}
	resolved, err := svc.Explain(hook, "", nil)
	if err != nil {
		return fail(stderr, err)
	}
	// Publish to the attach socket alongside the local TUI, so the run can also
	// be watched from another terminal.
	server := startAttach(svc)
	defer server.Close()
	start := time.Now()
	res, err := tui.Run(ctx, svc, br, hook, resolved.Steps, server)
	if err != nil {
		return fail(stderr, err)
	}
	server.PublishDone(res)
	maybeNotify(svc, res, time.Since(start))
	// The TUI already rendered the outcome as its final frame — don't reprint
	// it. Pre-push always exits non-zero so git's own (stale) push is stopped.
	return 1
}

// notifyAfter is the DEFAULT run duration above which a passing interactive
// pre-push is worth a desktop notification, used when the repo doesn't set
// `notify_after`. Shorter passing runs finish while the developer is still
// watching the terminal, so a notification then is pure noise — the point is to
// reach someone who tabbed away during a *long* gate.
const notifyAfter = 10 * time.Second

// notifyThreshold resolves the passing-run notification threshold: the repo's
// `notify_after` (e.g. "30s", "2m") when set and parseable, otherwise the
// notifyAfter default. A malformed value falls back rather than erroring — a
// bad duration should never wedge a push.
func notifyThreshold(cfg domain.Config) time.Duration {
	if cfg.NotifyAfter != "" {
		if d, err := time.ParseDuration(cfg.NotifyAfter); err == nil && d >= 0 {
			return d
		}
	}
	return notifyAfter
}

// shouldNotify reports whether a finished run warrants a desktop notification.
// Notifications are on unless the repo set `notify: false`. A failed/blocked
// push ALWAYS notifies — you never want to miss a gate that stopped your push,
// however fast it failed. A passing run notifies only once it ran long enough
// (notifyThreshold) that the developer may have looked away, so fast green gates
// stay silent. Pure and side-effect-free so the policy is unit-testable.
func shouldNotify(cfg domain.Config, outcome domain.Outcome, elapsed time.Duration) bool {
	if cfg.Notify != nil && !*cfg.Notify {
		return false
	}
	if outcome != domain.OutcomePassed {
		return true
	}
	return elapsed >= notifyThreshold(cfg)
}

// maybeNotify fires a desktop notification with the run's verdict when the run
// was long enough to have lost the developer's attention (see shouldNotify), so
// someone who tabbed away during a long pre-push learns the outcome — without
// spamming a notification after every fast gate.
func maybeNotify(svc interface{ Config() (domain.Config, error) }, res application.RunResult, elapsed time.Duration) {
	cfg, err := svc.Config()
	if err != nil {
		return
	}
	if !shouldNotify(cfg, res.Outcome, elapsed) {
		return
	}
	title := "warden: passed"
	if res.Outcome != domain.OutcomePassed {
		title = "warden: " + string(res.Outcome)
	}
	notify.Send(title, res.Message)
}

// runPreCommitExit re-applies any auto-fixes to the live tree and exits 0 on a
// pass so the commit proceeds; a failure exits non-zero to abort the commit.
func runPreCommitExit(svc interface{ ApplyFixPatch(string) error }, res application.RunResult, stdout, stderr io.Writer) int {
	if res.Outcome != domain.OutcomePassed {
		fmt.Fprintf(stderr, "warden: %s\n", res.Message)
		return 1
	}
	if res.FixPatch != "" {
		if err := svc.ApplyFixPatch(res.FixPatch); err != nil {
			return fail(stderr, fmt.Errorf("re-apply fixes: %w", err))
		}
		fmt.Fprintln(stdout, "warden: applied auto-fixes to your working tree.")
	}
	fmt.Fprintln(stdout, "warden: pre-commit passed.")
	return 0
}

// runPrePushExit reports the outcome and always returns non-zero (see cmdRun).
func runPrePushExit(res application.RunResult, stdout io.Writer) int {
	fmt.Fprintf(stdout, "warden: %s\n", res.Message)
	return 1
}

func printFindings(w io.Writer, findings []domain.Finding) {
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(w, "  [%s] %s %s\n", f.Severity, loc, f.Message)
	}
}
