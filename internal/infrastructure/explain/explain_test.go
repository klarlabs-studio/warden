package explain

import (
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// parseChart renders and unmarshals a chart, failing the test on any error, so
// individual cases can assert on structure without repeating boilerplate.
func parseChart(t *testing.T, p domain.ResolvedPolicy) map[string]any {
	t.Helper()
	out, err := Chart(p)
	if err != nil {
		t.Fatalf("Chart() error = %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("Chart() produced invalid JSON: %v\n%s", err, out)
	}
	return m
}

// states returns the top-level states map from a parsed chart.
func states(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	s, ok := m["states"].(map[string]any)
	if !ok {
		t.Fatalf("chart has no states object: %v", m)
	}
	return s
}

func TestChart_PrePushSequence(t *testing.T) {
	p := domain.ResolvedPolicy{
		Hook:  domain.PrePush,
		Steps: domain.DefaultSteps(domain.PrePush),
		Agents: map[domain.StepName]string{
			domain.StepReview: "claude-code",
		},
		AutoFix: map[domain.StepName]int{
			domain.StepLint: 3,
		},
	}
	m := parseChart(t, p)
	s := states(t, m)

	// Every step must appear as its own state.
	for _, step := range p.Steps {
		if _, ok := s[string(step)]; !ok {
			t.Errorf("missing state for step %q", step)
		}
	}

	// A pre-push run ends in "pushed" and not "committed".
	if _, ok := s["pushed"]; !ok {
		t.Errorf("pre-push chart missing terminal 'pushed' state")
	}
	if _, ok := s["committed"]; ok {
		t.Errorf("pre-push chart must not contain 'committed' state")
	}

	// No approval state when approval is not required.
	if _, ok := s["awaiting_approval"]; ok {
		t.Errorf("chart must not contain 'awaiting_approval' when RequireApproval is false")
	}

	// Initial state is the first step.
	if got := m["initial"]; got != string(p.Steps[0]) {
		t.Errorf("initial = %v, want %v", got, p.Steps[0])
	}

	// The resolved agent and budget are encoded in the step entry actions.
	raw, err := Chart(p)
	if err != nil {
		t.Fatalf("Chart() error = %v", err)
	}
	if !strings.Contains(raw, "agent=claude-code") {
		t.Errorf("chart does not document the resolved review agent:\n%s", raw)
	}
	if !strings.Contains(raw, "autofix=3") {
		t.Errorf("chart does not document the lint auto-fix budget:\n%s", raw)
	}
}

func TestChart_PreCommitTerminal(t *testing.T) {
	p := domain.ResolvedPolicy{
		Hook:  domain.PreCommit,
		Steps: domain.DefaultSteps(domain.PreCommit),
	}
	s := states(t, parseChart(t, p))

	if _, ok := s["committed"]; !ok {
		t.Errorf("pre-commit chart missing terminal 'committed' state")
	}
	if _, ok := s["pushed"]; ok {
		t.Errorf("pre-commit chart must not contain 'pushed' state")
	}
}

func TestChart_RequireApproval(t *testing.T) {
	p := domain.ResolvedPolicy{
		Hook:            domain.PrePush,
		Steps:           []domain.StepName{domain.StepLint},
		RequireApproval: true,
	}
	m := parseChart(t, p)
	s := states(t, m)

	if _, ok := s["awaiting_approval"]; !ok {
		t.Fatalf("chart missing 'awaiting_approval' when RequireApproval is true")
	}

	// The approval state must sit before the terminal: it transitions to
	// "pushed" on pass.
	approval, _ := s["awaiting_approval"].(map[string]any)
	on, _ := approval["on"].(map[string]any)
	pass, _ := on["pass"].(map[string]any)
	if pass["target"] != "pushed" {
		t.Errorf("awaiting_approval should transition to 'pushed', got %v", pass["target"])
	}
}

func TestChart_NoApprovalWhenFalse(t *testing.T) {
	p := domain.ResolvedPolicy{
		Hook:            domain.PrePush,
		Steps:           []domain.StepName{domain.StepLint},
		RequireApproval: false,
	}
	if _, ok := states(t, parseChart(t, p))["awaiting_approval"]; ok {
		t.Errorf("chart must not contain 'awaiting_approval' when RequireApproval is false")
	}
}

// TestChart_EmptySteps guards the degenerate case: an empty pipeline still
// yields a valid machine that goes straight to its terminal state.
func TestChart_EmptySteps(t *testing.T) {
	m := parseChart(t, domain.ResolvedPolicy{Hook: domain.PrePush})
	s := states(t, m)

	if m["initial"] != "pushed" {
		t.Errorf("empty pre-push chart initial = %v, want 'pushed'", m["initial"])
	}
	if _, ok := s["pushed"]; !ok {
		t.Errorf("empty chart missing terminal 'pushed' state")
	}
}

// TestChart_EmptyStepsWithApproval ensures approval is honored even with no
// steps: the run begins paused for sign-off, then completes.
func TestChart_EmptyStepsWithApproval(t *testing.T) {
	m := parseChart(t, domain.ResolvedPolicy{Hook: domain.PreCommit, RequireApproval: true})
	s := states(t, m)

	if m["initial"] != "awaiting_approval" {
		t.Errorf("initial = %v, want 'awaiting_approval'", m["initial"])
	}
	if _, ok := s["committed"]; !ok {
		t.Errorf("missing terminal 'committed' state")
	}
}
