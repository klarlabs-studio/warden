package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// terminalApprover resolves the run's approval gate interactively. Clean runs
// never reach it (the runner auto-passes those); it is consulted only when a
// rule required approval or a step flagged findings.
//
// It asks on stdin/stdout when stdin is a terminal (`warden run` typed by
// hand). Under `git push` stdin is git's ref list, so it asks on the
// controlling terminal instead, as git hooks conventionally do. With neither
// (CI, launchd, a process spawned without a terminal) it declines, so a gate
// that demands sign-off is never silently waved through.
//
// A terminal answering is all this establishes. An agent that drives a pty can
// answer too; ADR 0007 records that limit.
type terminalApprover struct {
	in  io.Reader
	out io.Writer
	tty bool
	// openTTY opens the controlling terminal; nil, or an error, means there is
	// none. Injected so tests need no real terminal.
	openTTY func() (io.ReadWriteCloser, error)
	// ttyTimeout bounds how long the controlling-terminal prompt waits. A
	// process with a controlling terminal nobody is watching must not hang the
	// push forever; no answer is a decline.
	ttyTimeout time.Duration
}

// controllingTTYTimeout is how long a `git push` waits at the approval prompt.
const controllingTTYTimeout = 5 * time.Minute

func newTerminalApprover(in io.Reader, out io.Writer) terminalApprover {
	return terminalApprover{
		in:         in,
		out:        out,
		tty:        isatty.IsTerminal(os.Stdin.Fd()),
		openTTY:    openControllingTTY,
		ttyTimeout: controllingTTYTimeout,
	}
}

func (a terminalApprover) Approve(ctx context.Context, req application.ApprovalRequest) (application.Decision, error) {
	if a.tty {
		return ask(ctx, a.in, a.out, req, 0, "interactive terminal decision"), nil
	}
	if a.openTTY != nil {
		if t, err := a.openTTY(); err == nil {
			defer func() { _ = t.Close() }()
			return ask(ctx, t, t, req, a.ttyTimeout, "controlling terminal decision"), nil
		}
	}
	describeApproval(a.out, req)
	hint := "Approve by running `git push` from an interactive terminal, or via `warden axi`."
	if !agentSurfaceMayApprove(req.Profile) {
		hint = "Approve by running `git push` from an interactive terminal."
	}
	_, _ = fmt.Fprintln(a.out, "  non-interactive stream and no terminal to ask; declining. "+hint)
	return application.Decision{Approved: false, Principal: "warden-cli", Rationale: "no tty"}, nil
}

// describeApproval writes what the gate is asking about.
func describeApproval(w io.Writer, req application.ApprovalRequest) {
	_, _ = fmt.Fprintf(w, "\nwarden: %s on %s needs approval (risk=%s)\n", req.Hook, req.Branch, req.Risk)
	for _, f := range req.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		_, _ = fmt.Fprintf(w, "  [%s] %s %s\n", f.Severity, loc, f.Message)
	}
}

// ask prompts on w and reads one answer from r. Only "y" approves. A timeout
// (when positive), a cancelled run, or a read error declines.
func ask(ctx context.Context, r io.Reader, w io.Writer, req application.ApprovalRequest, timeout time.Duration, rationale string) application.Decision {
	describeApproval(w, req)
	_, _ = fmt.Fprint(w, "  approve? [y/N] ")
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		answer <- line
	}()
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	declined := application.Decision{Approved: false, Principal: "warden-cli"}
	select {
	case line := <-answer:
		return application.Decision{
			Approved:  strings.EqualFold(strings.TrimSpace(line), "y"),
			Principal: "warden-cli",
			Rationale: rationale,
			// Either path is a terminal answering: stdin, or the controlling
			// terminal under git push. Neither proves a person did.
			Channel: domain.ApprovalTTY,
		}
	case <-expired:
		_, _ = fmt.Fprintln(w, "\n  no answer; declining.")
		declined.Rationale = "no answer at the terminal"
		return declined
	case <-ctx.Done():
		declined.Rationale = "run cancelled at the approval prompt"
		return declined
	}
}

// agentSurfacePrincipal names the agent surfaces (MCP, axi) on a decision they
// returned without approving.
const agentSurfacePrincipal = "warden-agent-surface"

// agentSurfaceApprover answers the approval gate for runs an agent starts
// through MCP or axi. Without a profile, and under permissive, it approves
// every gate, as these surfaces always have. Under a profile that withholds
// approval from the agent surface (standard, strict) it does not decide: the
// agent that asked for the run cannot also be the person who approves it, so
// the run ends needs_approval with nothing pushed and no note written.
type agentSurfaceApprover struct{}

func (agentSurfaceApprover) Approve(ctx context.Context, req application.ApprovalRequest) (application.Decision, error) {
	if agentSurfaceMayApprove(req.Profile) {
		return autoApprover{}.Approve(ctx, req)
	}
	return application.Decision{
		NeedsHuman: true,
		Principal:  agentSurfacePrincipal,
		Rationale:  agentApprovalRequired(req),
	}, nil
}

// agentSurfaceMayApprove reports whether the agent surface may satisfy an
// approval gate under profile. No profile keeps today's auto-approval. A
// profile this warden cannot parse fails closed: the policy named it, so
// guessing that it permits self-approval would weaken it.
func agentSurfaceMayApprove(profile *domain.ResolvedProfile) bool {
	if profile == nil {
		return true
	}
	p, err := domain.ParseComplianceProfile(profile.Name)
	if err != nil {
		return false
	}
	return p.AgentSurfaceMayApprove()
}

// agentApprovalRequired is the message an agent reads when its run stopped at
// the approval gate: what happened, what did not, and how a person approves.
func agentApprovalRequired(req application.ApprovalRequest) string {
	how := "`git push`"
	if req.Hook == domain.PreCommit {
		how = "`git commit`"
	}
	return fmt.Sprintf(
		"needs human approval: profile %s does not let an agent surface (MCP, axi) approve its own run, so nothing was pushed and no note was written. A person must approve: run %s from an interactive terminal and answer the approval prompt",
		req.Profile.Name, how)
}
