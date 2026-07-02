package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestFmtDur(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "0.0s",
		1500 * time.Millisecond: "1.5s",
		-time.Second:            "0.0s",
		75 * time.Second:        "1m15s",
		125 * time.Second:       "2m05s",
	}
	for d, want := range cases {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestView_ShowsStepDurationsAndTotal(t *testing.T) {
	m := newModel(domain.PrePush, []domain.StepName{"lint", "test"}, make(chan tea.Msg, 4))
	base := time.Now()
	m.start = base
	// lint took 2.0s (finished), test still running for 1.0s so far.
	m.steps[0] = stepView{name: "lint", status: stepPass, started: base, finished: base.Add(2 * time.Second)}
	m.steps[1] = stepView{name: "test", status: stepRunning, started: base.Add(2 * time.Second)}
	m.now = base.Add(3 * time.Second)

	v := m.View()
	if !strings.Contains(v, "lint") || !strings.Contains(v, "2.0s") {
		t.Errorf("completed step should show its duration:\n%s", v)
	}
	if !strings.Contains(v, "1.0s") {
		t.Errorf("running step should count up:\n%s", v)
	}

	// Terminal frame shows total.
	next, _ := m.Update(doneMsg{res: application.RunResult{Outcome: domain.OutcomePassed, Hook: domain.PrePush}})
	if !strings.Contains(next.(model).View(), "total ") {
		t.Errorf("done frame should show a total")
	}
}
