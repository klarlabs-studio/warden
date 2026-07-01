package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"go.klarlabs.de/warden/internal/application"
)

// terminalApprover resolves the run's approval gate interactively. Clean runs
// never reach it (the runner auto-passes those); it is consulted only when a
// rule required approval or a step flagged findings. On a non-interactive
// stream it declines, so a gate that demands human sign-off is never silently
// waved through in CI — the operator must approve at a terminal or via an agent
// surface.
type terminalApprover struct {
	in  io.Reader
	out io.Writer
	tty bool
}

func newTerminalApprover(in io.Reader, out io.Writer) terminalApprover {
	return terminalApprover{in: in, out: out, tty: isatty.IsTerminal(os.Stdin.Fd())}
}

func (a terminalApprover) Approve(_ context.Context, req application.ApprovalRequest) (application.Decision, error) {
	fmt.Fprintf(a.out, "\nwarden: %s on %s needs approval (risk=%s)\n", req.Hook, req.Branch, req.Risk)
	for _, f := range req.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(a.out, "  [%s] %s %s\n", f.Severity, loc, f.Message)
	}
	if !a.tty {
		fmt.Fprintln(a.out, "  non-interactive stream; declining. Approve at a terminal or via `warden axi`.")
		return application.Decision{Approved: false, Principal: "warden-cli", Rationale: "no tty"}, nil
	}
	fmt.Fprint(a.out, "  approve? [y/N] ")
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	yes := strings.EqualFold(strings.TrimSpace(line), "y")
	return application.Decision{
		Approved:  yes,
		Principal: "warden-cli",
		Rationale: "interactive terminal decision",
	}, nil
}
