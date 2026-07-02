// Package tui renders an interactive, live view of a warden run: each pipeline
// step updates in place as it runs, findings stream in, and the approval gate
// is answered inline. It is the interactive counterpart to the plain stdout
// path — used only on a real terminal.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// --- bridge: connects the synchronous runner to the async TUI ---------------

// bridge implements application.Observer and application.Approver, forwarding
// step events and approval requests to the bubbletea program over a channel.
type bridge struct {
	events chan tea.Msg
}

func (b *bridge) OnStep(e application.StepEvent) {
	b.events <- stepMsg(e)
}

// Approve posts the request to the UI and blocks (on the runner's goroutine)
// until the model answers or the context is cancelled.
func (b *bridge) Approve(ctx context.Context, req application.ApprovalRequest) (application.Decision, error) {
	resp := make(chan application.Decision, 1)
	b.events <- approvalMsg{req: req, resp: resp}
	select {
	case d := <-resp:
		return d, nil
	case <-ctx.Done():
		return application.Decision{}, ctx.Err()
	}
}

// --- messages ---------------------------------------------------------------

type stepMsg application.StepEvent

type approvalMsg struct {
	req  application.ApprovalRequest
	resp chan application.Decision
}

type doneMsg struct {
	res application.RunResult
	err error
}

// --- runner hook ------------------------------------------------------------

// Runner is the subset of the service the TUI drives. The service satisfies it.
type Runner interface {
	SetObserver(application.Observer)
	Run(ctx context.Context, hook domain.Hook) (application.RunResult, error)
	Explain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error)
}

// Run drives an interactive gate run under a live TUI and returns the outcome.
// approverInto wires the bridge as the run's approver: the caller builds the
// service with it. steps is the resolved step order used to seed the display.
func Run(svc Runner, br application.Approver, hook domain.Hook, steps []domain.StepName) (application.RunResult, error) {
	b := br.(*bridge)
	svc.SetObserver(b)

	m := newModel(hook, steps, b.events)
	go func() {
		res, err := svc.Run(context.Background(), hook)
		b.events <- doneMsg{res: res, err: err}
	}()

	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return application.RunResult{}, err
	}
	fm := final.(model)
	return fm.result, fm.runErr
}

// NewApprover returns a bridge usable as both the run's Approver (passed to the
// service) and, inside Run, the Observer. Keeping one object means step events
// and approvals share the same channel and ordering.
func NewApprover() application.Approver {
	return &bridge{events: make(chan tea.Msg, 32)}
}

// --- helpers shared with the model -----------------------------------------

var (
	styPass    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	styFail    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	styRun     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
	styMuted   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styHeading = lipgloss.NewStyle().Bold(true)
)
