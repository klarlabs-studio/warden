package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
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

	svc, err := newService(newTerminalApprover(os.Stdin, stdout))
	if err != nil {
		return fail(stderr, err)
	}

	res, err := svc.Run(context.Background(), hook)
	if err != nil {
		return fail(stderr, err)
	}
	printFindings(stdout, res.Findings)

	switch hook {
	case domain.PreCommit:
		return runPreCommitExit(svc, res, stdout, stderr)
	default:
		return runPrePushExit(res, stdout)
	}
}

// runPreCommitExit re-applies any auto-fixes to the live tree and exits 0 on a
// pass so the commit proceeds; a failure exits non-zero to abort the commit.
func runPreCommitExit(svc interface{ ApplyFixPatch(string) error }, res application.RunResult, stdout, stderr io.Writer) int {
	if res.Outcome != application.OutcomePassed {
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
