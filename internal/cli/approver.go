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
	"go.klarlabs.de/warden/internal/domain"
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
	_, _ = fmt.Fprintf(a.out, "\nwarden: %s on %s needs approval (risk=%s)\n", req.Hook, req.Branch, req.Risk)
	for _, f := range req.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		_, _ = fmt.Fprintf(a.out, "  [%s] %s %s\n", f.Severity, loc, f.Message)
	}
	if !a.tty {
		hint := "Approve at a terminal or via `warden axi`."
		if !agentSurfaceMayApprove(req.Profile) {
			hint = "Approve at an interactive terminal."
		}
		_, _ = fmt.Fprintln(a.out, "  non-interactive stream; declining. "+hint)
		return application.Decision{Approved: false, Principal: "warden-cli", Rationale: "no tty"}, nil
	}
	_, _ = fmt.Fprint(a.out, "  approve? [y/N] ")
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	yes := strings.EqualFold(strings.TrimSpace(line), "y")
	return application.Decision{
		Approved:  yes,
		Principal: "warden-cli",
		Rationale: "interactive terminal decision",
	}, nil
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
	how := fmt.Sprintf("`warden run %s`", req.Hook)
	if req.Hook == domain.PrePush {
		how = "`git push` or `warden run pre-push`"
	}
	return fmt.Sprintf(
		"needs human approval: profile %s does not let an agent surface (MCP, axi) approve its own run, so nothing was pushed and no note was written. A person must approve: run the gate from an interactive terminal (%s) and answer the approval prompt",
		req.Profile.Name, how)
}
