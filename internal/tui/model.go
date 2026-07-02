package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// spinnerFrames animate the running step; tickInterval drives the redraw.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const tickInterval = 100 * time.Millisecond

// tickMsg drives the spinner/elapsed animation.
type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

type stepStatus int

const (
	stepPending stepStatus = iota
	stepRunning
	stepPass
	stepFail
)

type stepView struct {
	name   domain.StepName
	status stepStatus
}

type phase int

const (
	phaseRunning phase = iota
	phaseApproving
	phaseDone
)

// model is the bubbletea model for a live run.
type model struct {
	events   chan tea.Msg
	hook     domain.Hook
	steps    []stepView
	findings []domain.Finding
	phase    phase

	// approval gate state, set while phaseApproving.
	approval application.ApprovalRequest
	resp     chan application.Decision

	// frame advances every tick to animate the spinner; runStart is the frame
	// the current step began running, for its elapsed time.
	frame    int
	runStart int

	result application.RunResult
	runErr error
}

func newModel(hook domain.Hook, steps []domain.StepName, events chan tea.Msg) model {
	views := make([]stepView, len(steps))
	for i, s := range steps {
		views[i] = stepView{name: s, status: stepPending}
	}
	return model{events: events, hook: hook, steps: views, phase: phaseRunning}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.listen(), tick()) }

// listen blocks on the events channel and delivers the next message, so the
// synchronous runner's events drive the async UI.
func (m model) listen() tea.Cmd {
	return func() tea.Msg { return <-m.events }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case stepMsg:
		m.applyStep(application.StepEvent(msg))
		return m, m.listen()

	case approvalMsg:
		m.phase = phaseApproving
		m.approval = msg.req
		m.resp = msg.resp
		return m, m.listen()

	case doneMsg:
		m.result = msg.res
		m.runErr = msg.err
		m.phase = phaseDone
		return m, tea.Quit

	case tickMsg:
		m.frame++
		return m, tick() // keep animating until the run completes (Quit stops it)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) applyStep(e application.StepEvent) {
	for i := range m.steps {
		if m.steps[i].name != e.Step {
			continue
		}
		if e.Phase == application.StepStarted {
			m.steps[i].status = stepRunning
			m.runStart = m.frame
			return
		}
		if e.Result.Status == domain.StepFail {
			m.steps[i].status = stepFail
		} else {
			m.steps[i].status = stepPass
		}
		m.findings = append(m.findings, e.Result.Findings...)
		return
	}
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if m.phase != phaseApproving {
		return m, m.listen()
	}
	switch strings.ToLower(k.String()) {
	case "y":
		m.resp <- application.Decision{Approved: true, Principal: "warden-tui", Rationale: "approved in TUI"}
		m.phase = phaseRunning
		return m, m.listen()
	case "n":
		m.resp <- application.Decision{Approved: false, Principal: "warden-tui", Rationale: "declined in TUI"}
		m.phase = phaseRunning
		return m, m.listen()
	}
	return m, m.listen()
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(styHeading.Render("warden "+string(m.hook)) + "\n\n")

	var running domain.StepName
	for _, s := range m.steps {
		line := "  " + m.stepGlyph(s.status) + " " + string(s.name)
		if s.status == stepRunning {
			running = s.name
			elapsed := float64(m.frame-m.runStart) * tickInterval.Seconds()
			line = styRun.Render("  "+spinnerFrames[m.frame%len(spinnerFrames)]+" "+string(s.name)) +
				styMuted.Render(fmt.Sprintf("  %.1fs", elapsed))
		}
		b.WriteString(line + "\n")
	}

	if len(m.findings) > 0 {
		b.WriteString("\n" + styHeading.Render("findings") + "\n")
		for _, f := range m.findings {
			b.WriteString("  " + renderFinding(f) + "\n")
		}
	}

	switch m.phase {
	case phaseApproving:
		b.WriteString("\n" + styRun.Render(fmt.Sprintf("approval required (risk=%s) — approve? [y/N]", m.approval.Risk)) + "\n")
	case phaseDone:
		b.WriteString("\n" + renderOutcome(m.result) + "\n")
	default:
		hint := "working…"
		if running != "" {
			hint = string(running) + "…"
		}
		b.WriteString("\n" + styMuted.Render(spinnerFrames[m.frame%len(spinnerFrames)]+" "+hint+"   (ctrl+c to abort)") + "\n")
	}
	return b.String()
}

// stepGlyph renders a non-running step's status marker. The running step is
// drawn with the animated spinner in View.
func (m model) stepGlyph(s stepStatus) string {
	switch s {
	case stepRunning:
		return styRun.Render(spinnerFrames[m.frame%len(spinnerFrames)])
	case stepPass:
		return styPass.Render("✓")
	case stepFail:
		return styFail.Render("✗")
	default:
		return styMuted.Render("○")
	}
}

func renderFinding(f domain.Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return styMuted.Render(fmt.Sprintf("[%s] %s %s", f.Severity, loc, f.Message))
}

func renderOutcome(res application.RunResult) string {
	style := styPass
	if res.Outcome != domain.OutcomePassed {
		style = styFail
	}
	return style.Render(string(res.Outcome)) + " — " + res.Message
}
