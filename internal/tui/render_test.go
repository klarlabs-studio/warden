package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// TestRenderStages drives a model through a full pre-push run and asserts each
// rendered frame, then prints the frames so the TUI can be eyeballed headlessly
// (go test -v -run RenderStages ./internal/tui/).
func TestRenderStages(t *testing.T) {
	steps := []domain.StepName{"rebase", "lint", "security-scan", "test"}
	m := newModel(domain.PrePush, steps, make(chan tea.Msg, 16))

	frame := func(label string) string {
		out := m.View()
		fmt.Printf("\n──── %s ────\n%s", label, out)
		return out
	}

	// 1) Initial — everything pending, animated footer.
	if f := frame("initial"); !strings.Contains(f, "rebase") || !strings.Contains(f, "ctrl+c") {
		t.Errorf("initial frame missing steps/footer:\n%s", f)
	}

	// 2) Mid-run — rebase passed, lint running.
	m = apply(m, stepMsg{Step: "rebase", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "rebase", Status: domain.StepPass}})
	m = apply(m, stepMsg{Step: "lint", Phase: application.StepStarted})
	if f := frame("mid-run"); !strings.Contains(f, "rebase") {
		t.Errorf("mid-run frame wrong:\n%s", f)
	}

	// 3) A failing finding surfaces.
	m = apply(m, stepMsg{Step: "lint", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "lint", Status: domain.StepPass,
			Findings: []domain.Finding{{Severity: domain.SeverityMedium, File: "auth/token.go", Line: 42, Message: "unchecked error"}}}})
	if f := frame("with-finding"); !strings.Contains(f, "auth/token.go:42") {
		t.Errorf("finding not rendered:\n%s", f)
	}

	// 4) Approval gate.
	m.phase = phaseApproving
	m.approval = application.ApprovalRequest{Risk: domain.RiskHigh}
	if f := frame("approval"); !strings.Contains(f, "approve? [y/N]") {
		t.Errorf("approval prompt not rendered:\n%s", f)
	}

	// 5) Final outcome.
	m = apply(m, doneMsg{res: application.RunResult{Outcome: domain.OutcomePassed, Hook: domain.PrePush,
		Message: "warden pushed the gated commit(s); PR https://github.com/o/r/pull/7"}})
	if f := frame("done"); !strings.Contains(f, "passed") || !strings.Contains(f, "pull/7") {
		t.Errorf("final frame wrong:\n%s", f)
	}
}

func apply(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func TestOutputTail_ShownWhileRunningClearedWhenDone(t *testing.T) {
	steps := []domain.StepName{"test"}
	m := newModel(domain.PrePush, steps, make(chan tea.Msg, 16))

	m = apply(m, stepMsg{Step: "test", Phase: application.StepStarted})
	m = apply(m, stepMsg{Step: "test", Phase: application.StepOutput, Line: "=== RUN TestFoo"})
	m = apply(m, stepMsg{Step: "test", Phase: application.StepOutput, Line: "--- PASS: TestFoo"})

	// Only the most recent line is tailed under the running step.
	if f := m.View(); strings.Contains(f, "RUN TestFoo") || !strings.Contains(f, "PASS: TestFoo") {
		t.Errorf("expected only the latest output line tailed:\n%s", f)
	}

	// Finishing the step drops its tail.
	m = apply(m, stepMsg{Step: "test", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "test", Status: domain.StepPass}})
	if f := m.View(); strings.Contains(f, "PASS: TestFoo") {
		t.Errorf("a finished step must not keep showing its output tail:\n%s", f)
	}
}

func TestFindings_CollapseToggle(t *testing.T) {
	steps := []domain.StepName{"lint"}
	m := newModel(domain.PrePush, steps, make(chan tea.Msg, 16))
	m = apply(m, stepMsg{Step: "lint", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "lint", Status: domain.StepPass,
			Findings: []domain.Finding{{Severity: domain.SeverityMedium, File: "auth/token.go", Line: 42, Message: "unchecked error"}}}})

	// Expanded by default: the finding and the controls hint show.
	if f := m.View(); !strings.Contains(f, "auth/token.go:42") || !strings.Contains(f, "1-9 open") {
		t.Errorf("expanded findings view wrong:\n%s", f)
	}

	// Press f → collapsed: a count line, no finding detail.
	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	f := m.View()
	if !strings.Contains(f, "findings (1)") || !strings.Contains(f, "press f to expand") {
		t.Errorf("collapsed findings view missing count/hint:\n%s", f)
	}
	if strings.Contains(f, "auth/token.go:42") {
		t.Errorf("collapsed view must hide finding detail:\n%s", f)
	}

	// Press f again → expanded.
	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if f := m.View(); !strings.Contains(f, "auth/token.go:42") {
		t.Errorf("re-expanded view should show the finding again:\n%s", f)
	}
}

func TestTruncateLine(t *testing.T) {
	if got := truncateLine("short", 72); got != "short" {
		t.Errorf("short line changed: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := truncateLine(long, 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateLine(len100,10) = %q (len %d)", got, len([]rune(got)))
	}
}
