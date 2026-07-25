package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/attach"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/notify"
	"go.klarlabs.de/warden/internal/tui"
)

// cmdRun handles `warden run <hook>`, the entry point the installed hook shims
// call. Its exit code drives git: a pre-commit pass exits 0 so the commit
// proceeds; a pre-push pass exits 0 when warden stood aside and left the push to
// git, and non-zero when warden performed the push itself and git's own
// (now-stale) push must be stopped from racing it (§4.3 step 4). A blocked push
// always exits non-zero. See runPrePushExit.
func cmdRun(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: warden run <pre-commit|pre-push>")
		return 2
	}
	hook, err := domain.ParseHook(args[0])
	if err != nil {
		return fail(stderr, err)
	}

	// Git feeds a pre-push hook the refs being pushed on stdin; when the push
	// advances no branch — a notes-only push (e.g. refs/notes/warden), a tag, a
	// lone branch deletion, an unrelated ref — there is nothing to gate, so let
	// git complete the push instead of re-running the whole pipeline (and firing a
	// spurious notification). Only read when stdin is not a terminal: a real push
	// pipes the ref list, whereas a manual `warden run pre-push` has an interactive
	// stdin we must not block on — there we gate as before. A parse error or empty
	// payload falls through to gating (fail safe toward enforcement).
	if hook == domain.PrePush && !isatty.IsTerminal(os.Stdin.Fd()) {
		if gatable, err := pushGatable(os.Stdin); err == nil && !gatable {
			fmt.Fprintln(stdout, "warden: push advances no branch; nothing to gate.")
			return 0
		}
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

// pushGatable reports whether a git pre-push stdin payload gives warden something
// to gate. It does when at least one branch (refs/heads/*) is being created or
// updated. Git feeds a pre-push hook one line per pushed ref:
//
//	<local ref> SP <local sha> SP <remote ref> SP <remote sha>
//
// A deletion carries an all-zero local sha, so it advances nothing. A push that
// advances no branch (notes, tags, deletions, unrelated refs) has nothing to
// gate — return false so the caller lets git complete it. Fail safe toward
// enforcement: an EMPTY or unreadable payload (a manual `warden run pre-push`, a
// test) returns true, so warden never skips a push it merely failed to parse;
// only an affirmatively-parsed, branchless ref set is skipped. Short/blank lines
// are ignored so a stray line can't wedge the hook.
func pushGatable(r io.Reader) (bool, error) {
	sc := bufio.NewScanner(r)
	sawRef := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		sawRef = true
		localSHA, remoteRef := fields[1], fields[2]
		if strings.HasPrefix(remoteRef, "refs/heads/") && !isZeroSHA(localSHA) {
			return true, nil
		}
	}
	if err := sc.Err(); err != nil {
		return true, err // unreadable payload: fail safe toward gating
	}
	// Refs seen but none advanced a branch → nothing to gate. No refs at all →
	// not a real push we can reason about → gate.
	return !sawRef, nil
}

// isZeroSHA reports whether s is a git null object id (all zeros), which a
// pre-push line uses for the local sha of a branch deletion.
func isZeroSHA(s string) bool {
	if s == "" {
		return false
	}
	return strings.Trim(s, "0") == ""
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
	// it. Exit 0 only when git is completing the push itself; otherwise warden
	// already pushed and must fail the hook so git's stale push is stopped.
	noteGitPushError(stdout, res)
	if res.Outcome == domain.OutcomePassed && res.GitCompletesPush {
		return 0
	}
	return 1
}

// noteGitPushError, on a SUCCESSFUL pre-push that warden pushed ITSELF, prints
// a heads-up that git is about to print "error: failed to push some refs".
//
// Warden has to fail the hook in that case: git pushes the ref value it
// captured before the hook ran, so letting it proceed after warden rewrote the
// branch would publish the unvalidated pre-fix commit. (Exiting 0 is worse
// still — git compare-and-swaps against its stale advertisement and the remote,
// already at the new value, hard-rejects it.) The deliberate non-zero exit is
// what makes git emit the error, so without this line a successful push ends on
// a red "error:".
//
// It stays silent when git is completing the push (nothing was rewritten): git
// then reports the real outcome and warden exits 0, so there is nothing to
// apologize for. On a real failure git's error is correct and stands.
func noteGitPushError(w io.Writer, res application.RunResult) {
	if res.Outcome != domain.OutcomePassed || res.GitCompletesPush {
		return
	}
	fmt.Fprintln(w, `warden: git will now print 'error: failed to push some refs' — that's expected, not a failure; warden already pushed your gated commit.`)
}

// notifyAfter is the DEFAULT run duration above which a passing interactive
// pre-push is worth a desktop notification, used when the repo doesn't set
// `notify_after`. Shorter passing runs finish while the developer is still
// watching the terminal, so a notification then is pure noise — the point is to
// reach someone who tabbed away during a *long* gate.
const notifyAfter = 10 * time.Second

// notifyThreshold resolves the passing-run notification threshold: the repo's
// `notify_after` (e.g. "30s", "2m") when set, otherwise the notifyAfter default.
// A malformed or negative value is rejected earlier at config load (see
// Config.Validate), so a loaded config always parses here; the fallback remains
// only as defense for a Config constructed programmatically past Validate.
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

// notifySource is the run context a notification needs to be worth reading:
// which repo and which branch the verdict is about.
type notifySource interface {
	Config() (domain.Config, error)
	Repo() *git.Repo
}

// buildNotification composes the desktop notification for a finished run. The
// verdict alone ("failed") is not enough to act on when it arrives minutes
// later on a machine with several repos open: the title says which hook reached
// which verdict, the subtitle scopes it to repo and branch, and the body
// carries the actionable detail (which step, or what was pushed). Pure, so the
// wording is unit-testable without a desktop.
func buildNotification(res application.RunResult, repo, branch string) notify.Notification {
	verdict := string(res.Outcome)
	n := notify.Notification{
		Title:  fmt.Sprintf("warden: %s %s", res.Hook, verdict),
		Body:   res.Message,
		Urgent: res.Outcome != domain.OutcomePassed,
		Group:  "warden-" + repo,
	}
	switch {
	case repo != "" && branch != "":
		n.Subtitle = repo + " · " + branch
	case repo != "":
		n.Subtitle = repo
	case branch != "":
		n.Subtitle = branch
	}
	// A bare "passed" says nothing about what stands behind it; name the checks,
	// the same way the terminal lines now do.
	if res.Outcome == domain.OutcomePassed && len(res.Policy.Steps) > 0 {
		n.Body = fmt.Sprintf("%s (%s)", n.Body, domain.JoinSteps(res.Policy.Steps))
	}
	if n.Body == "" {
		n.Body = verdict
	}
	return n
}

// maybeNotify fires a desktop notification with the run's verdict when the run
// was long enough to have lost the developer's attention (see shouldNotify), so
// someone who tabbed away during a long pre-push learns the outcome — without
// spamming a notification after every fast gate.
func maybeNotify(svc notifySource, res application.RunResult, elapsed time.Duration) {
	cfg, err := svc.Config()
	if err != nil {
		return
	}
	if !shouldNotify(cfg, res.Outcome, elapsed) {
		return
	}
	// Repo/branch are context, not correctness: a failure to read either costs
	// the subtitle, never the notification.
	var repo, branch string
	if r := svc.Repo(); r != nil {
		repo = filepath.Base(r.Dir)
		branch, _ = r.CurrentBranch()
	}
	notify.Send(buildNotification(res, repo, branch))
}

// preCommitReporter is the slice of the service a finished pre-commit needs:
// re-apply the fix patch, and read the configured step lists so the pass line
// can say what actually ran and what is still outstanding.
type preCommitReporter interface {
	ApplyFixPatch(string) error
	StepsList() (preCommit, prePush []domain.StepName, err error)
}

// passLine renders the pre-commit pass message. A bare "pre-commit passed"
// reads as "my tree is green" even under a split policy where only the fast
// checks ran, so the line names the steps it actually ran and — when the policy
// defers others to pre-push — says so in the same breath. Pure and
// side-effect-free so the wording is unit-testable. A run whose step list is
// unknown (empty policy, or an unreadable config) degrades to the original
// unqualified line rather than asserting something it cannot back up.
func passLine(ran, prePush []domain.StepName) string {
	if len(ran) == 0 {
		return "warden: pre-commit passed."
	}
	line := fmt.Sprintf("warden: pre-commit passed (%s)", domain.JoinSteps(ran))
	if deferred := domain.DeferredSteps(ran, prePush); len(deferred) > 0 {
		verb := "runs"
		if len(deferred) > 1 {
			verb = "run"
		}
		return fmt.Sprintf("%s — %s %s at pre-push.", line, domain.JoinSteps(deferred), verb)
	}
	return line + "."
}

// runPreCommitExit re-applies any auto-fixes to the live tree and exits 0 on a
// pass so the commit proceeds; a failure exits non-zero to abort the commit.
func runPreCommitExit(svc preCommitReporter, res application.RunResult, stdout, stderr io.Writer) int {
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
	// A config we can't read costs us the deferred-steps clause, not the pass:
	// reporting is never allowed to fail a run that the gate already passed.
	_, prePush, err := svc.StepsList()
	if err != nil {
		prePush = nil
	}
	fmt.Fprintln(stdout, passLine(res.Policy.Steps, prePush))
	return 0
}

// runPrePushExit reports the outcome and returns the hook's exit code. For
// symmetry with the pre-commit line the steps that ran are named, so a passing
// push says which checks stand behind it. Nothing is deferred past pre-push, so
// there is no follow-up clause to add.
//
// Exit 0 when git is completing the push: git then reports the real result and
// "did it push?" is answerable from the exit code alone. Otherwise warden
// already pushed and MUST fail the hook so git's stale push cannot proceed (see
// noteGitPushError) — the one case where a success still exits non-zero.
func runPrePushExit(res application.RunResult, stdout io.Writer) int {
	msg := res.Message
	if res.Outcome == domain.OutcomePassed && len(res.Policy.Steps) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, domain.JoinSteps(res.Policy.Steps))
	}
	fmt.Fprintf(stdout, "warden: %s\n", msg)
	noteGitPushError(stdout, res)
	if res.Outcome == domain.OutcomePassed && res.GitCompletesPush {
		return 0
	}
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
