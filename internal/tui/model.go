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
	name     domain.StepName
	status   stepStatus
	started  time.Time
	finished time.Time
	// lastLine is the most recent output line the step emitted, shown as a live
	// tail beneath a running step.
	lastLine string
}

// elapsed returns how long the step has run (live if still running).
func (s stepView) elapsed(now time.Time) time.Duration {
	if s.started.IsZero() {
		return 0
	}
	if s.status == stepRunning {
		return now.Sub(s.started)
	}
	return s.finished.Sub(s.started)
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

	// frame advances every tick to animate the spinner; now is refreshed each
	// tick so elapsed counters tick up. start/end bound the whole run.
	frame int
	now   time.Time
	start time.Time
	end   time.Time

	result application.RunResult
	runErr error
}

func newModel(hook domain.Hook, steps []domain.StepName, events chan tea.Msg) model {
	views := make([]stepView, len(steps))
	for i, s := range steps {
		views[i] = stepView{name: s, status: stepPending}
	}
	now := time.Now()
	return model{events: events, hook: hook, steps: views, phase: phaseRunning, now: now, start: now}
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
		m.end = time.Now()
		return m, tea.Quit

	case tickMsg:
		m.frame++
		m.now = time.Now() // refresh so the running step's counter ticks up
		return m, tick()   // keep animating until the run completes (Quit stops it)

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
			m.steps[i].started = time.Now()
			return
		}
		if e.Phase == application.StepOutput {
			if line := strings.TrimSpace(e.Line); line != "" {
				m.steps[i].lastLine = line
			}
			return
		}
		m.steps[i].finished = time.Now()
		m.steps[i].lastLine = "" // a finished step drops its live tail
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
		var line string
		if s.status == stepRunning {
			running = s.name
			line = styRun.Render("  "+spinnerFrames[m.frame%len(spinnerFrames)]+" "+string(s.name)) +
				styMuted.Render("  "+fmtDur(s.elapsed(m.now)))
			if s.lastLine != "" { // live output tail, dimmed and truncated
				line += "\n" + styMuted.Render("      "+truncateLine(s.lastLine, 72))
			}
		} else {
			line = "  " + m.stepGlyph(s.status) + " " + string(s.name)
			if !s.finished.IsZero() { // completed → show how long it took
				line += styMuted.Render("  " + fmtDur(s.elapsed(m.now)))
			}
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
		b.WriteString(styMuted.Render("total "+fmtDur(m.end.Sub(m.start))) + "\n")
	default:
		hint := "working…"
		if running != "" {
			hint = string(running) + "…"
		}
		b.WriteString("\n" + styMuted.Render(spinnerFrames[m.frame%len(spinnerFrames)]+" "+hint+"   (ctrl+c to abort)") + "\n")
	}
	return b.String()
}

// truncateLine clips s to max runes, adding an ellipsis, so a long output line
// never wraps and breaks the step list's layout.
func truncateLine(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width < 1 {
		return ""
	}
	return string(r[:width-1]) + "…"
}

// fmtDur formats a duration as a compact elapsed time: seconds under a minute
// (e.g. "1.4s"), else minutes+seconds ("1m03s").
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
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
