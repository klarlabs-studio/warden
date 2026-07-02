package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func step(m model, i int) stepStatus { return m.steps[i].status }

func TestModel_StepTransitions(t *testing.T) {
	events := make(chan tea.Msg, 8)
	m := newModel(domain.PrePush, []domain.StepName{"lint", "test"}, events)

	if step(m, 0) != stepPending || step(m, 1) != stepPending {
		t.Fatal("steps should start pending")
	}

	next, _ := m.Update(stepMsg{Step: "lint", Phase: application.StepStarted})
	m = next.(model)
	if step(m, 0) != stepRunning {
		t.Errorf("lint should be running, got %v", step(m, 0))
	}

	next, _ = m.Update(stepMsg{Step: "lint", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "lint", Status: domain.StepPass}})
	m = next.(model)
	if step(m, 0) != stepPass {
		t.Errorf("lint should be pass, got %v", step(m, 0))
	}

	next, _ = m.Update(stepMsg{Step: "test", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "test", Status: domain.StepFail,
			Findings: []domain.Finding{{Severity: domain.SeverityHigh, Message: "boom"}}}})
	m = next.(model)
	if step(m, 1) != stepFail {
		t.Errorf("test should be fail, got %v", step(m, 1))
	}
	if len(m.findings) != 1 || m.findings[0].Message != "boom" {
		t.Errorf("findings not collected: %+v", m.findings)
	}
}

func TestModel_ApprovalFlow(t *testing.T) {
	events := make(chan tea.Msg, 8)
	m := newModel(domain.PrePush, []domain.StepName{"review"}, events)
	resp := make(chan application.Decision, 1)

	next, _ := m.Update(approvalMsg{
		req:  application.ApprovalRequest{Risk: domain.RiskHigh},
		resp: resp,
	})
	m = next.(model)
	if m.phase != phaseApproving {
		t.Fatalf("phase = %v, want approving", m.phase)
	}
	if !strings.Contains(m.View(), "approve?") {
		t.Error("view should prompt for approval")
	}

	// Pressing 'y' approves and resumes.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = next.(model)
	select {
	case d := <-resp:
		if !d.Approved {
			t.Error("expected approval")
		}
	default:
		t.Fatal("no decision sent to resp channel")
	}
	if m.phase != phaseRunning {
		t.Errorf("phase after approve = %v, want running", m.phase)
	}
}

func TestModel_ApprovalDecline(t *testing.T) {
	m := newModel(domain.PrePush, nil, make(chan tea.Msg, 4))
	resp := make(chan application.Decision, 1)
	m.phase = phaseApproving
	m.resp = resp

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	_ = next
	d := <-resp
	if d.Approved {
		t.Error("'n' must decline")
	}
}

func TestModel_DoneQuits(t *testing.T) {
	m := newModel(domain.PrePush, []domain.StepName{"lint"}, make(chan tea.Msg, 4))
	res := application.RunResult{Outcome: domain.OutcomePassed, Hook: domain.PrePush, Message: "done"}
	next, cmd := m.Update(doneMsg{res: res})
	m = next.(model)
	if m.phase != phaseDone || m.result.Outcome != domain.OutcomePassed {
		t.Errorf("done not recorded: phase=%v result=%+v", m.phase, m.result)
	}
	if cmd == nil {
		t.Error("doneMsg should return a quit command")
	}
	if !strings.Contains(m.View(), "passed") {
		t.Errorf("final view should show outcome: %s", m.View())
	}
}
