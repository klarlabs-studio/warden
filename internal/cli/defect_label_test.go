package cli

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// Every defect must read as "do not trust this". The default arm matters most:
// an unrecognized defect is the case that arises when a new one is added to the
// domain and this switch is not updated, and a blank there would render as a
// clean row — the failure reading as a pass.
func TestNoteDefectLabel(t *testing.T) {
	for _, tc := range []struct {
		defect string
		want   string
	}{
		{domain.DefectChainBroken, "TAMPERED"},
		{domain.DefectUnbound, "UNBOUND"},
		{domain.DefectNoEvidence, "NO EVIDENCE"},
		{"a defect added later that nobody wired in here", "UNVERIFIED"},
		{"", "UNVERIFIED"},
	} {
		got := noteDefectLabel(tc.defect)
		if !strings.Contains(got, tc.want) {
			t.Errorf("noteDefectLabel(%q) = %q, want it to contain %q", tc.defect, got, tc.want)
		}
		// Whatever the defect, the label must never be empty — an empty cell in
		// the doctor table is indistinguishable from a verified commit.
		if strings.TrimSpace(got) == "" {
			t.Errorf("noteDefectLabel(%q) returned an empty label", tc.defect)
		}
	}
}

// stepObserver forwards only FINISHED steps. Output-line events are documented
// as best-effort and droppable under load, so forwarding them would hand a
// polling caller a partial transcript it could mistake for a complete one —
// the same class of defect as a check that reports green while measuring
// nothing.
func TestStepObserver_ForwardsOnlyFinishedSteps(t *testing.T) {
	var got []mcpserver.StepProgress
	obs := stepObserver{onStep: func(p mcpserver.StepProgress) { got = append(got, p) }}

	obs.OnStep(application.StepEvent{Step: "lint", Phase: application.StepStarted})
	obs.OnStep(application.StepEvent{Step: "lint", Phase: "output", Line: "partial output"})
	obs.OnStep(application.StepEvent{
		Step:   "lint",
		Phase:  application.StepFinished,
		Result: domain.StepResult{Status: "pass", Summary: "no findings"},
	})

	if len(got) != 1 {
		t.Fatalf("expected only the finished step to be forwarded, got %d: %+v", len(got), got)
	}
	if got[0].Step != "lint" || got[0].Status != "pass" || got[0].Summary != "no findings" {
		t.Errorf("finished step forwarded incorrectly: %+v", got[0])
	}
}
