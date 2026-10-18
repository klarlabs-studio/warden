package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// skippedReview is the run shape behind the defect: review is in the policy, no
// agent is configured, and the step passed without examining anything.
var skippedReview = map[domain.StepName]domain.OutcomeNote{
	domain.StepReview: {Outcome: domain.StepSkipped, Reason: "no agent configured"},
}

// Every line a person reads after a gate must say the review was skipped, and
// none may list it as a step that ran. The verdict (pass) and exit code stay.
func TestSkippedStep_HumanSurfacesSaySkipped(t *testing.T) {
	steps := []domain.StepName{domain.StepLint, domain.StepReview}
	const want = "lint; review: skipped, no agent configured"

	if got := passLine(steps, nil, skippedReview); got != "warden: pre-commit passed ("+want+")." {
		t.Errorf("passLine = %q", got)
	}

	res := application.RunResult{
		Outcome:          domain.OutcomePassed,
		Hook:             domain.PrePush,
		Policy:           domain.ResolvedPolicy{Steps: steps},
		Message:          "gate passed; git is completing the push",
		GitCompletesPush: true,
		StepNotes:        skippedReview,
	}
	var out bytes.Buffer
	if code := runPrePushExit(res, &out); code != 0 {
		t.Errorf("a skipped advisory step must not change the exit code, got %d", code)
	}
	if !strings.Contains(out.String(), "("+want+")") || strings.Contains(out.String(), "(lint, review)") {
		t.Errorf("pre-push line = %q", out.String())
	}

	if n := buildNotification(res, "repo", "main"); !strings.Contains(n.Body, want) {
		t.Errorf("notification body = %q", n.Body)
	}
}

// With nothing skipped or cached, every line reads exactly as before.
func TestSkippedStep_ExecutedRunsReadUnchanged(t *testing.T) {
	steps := []domain.StepName{domain.StepLint, domain.StepTest}
	if got := passLine(steps, nil, nil); got != "warden: pre-commit passed (lint, test)." {
		t.Errorf("passLine = %q", got)
	}
}

func TestStepObserver_ForwardsOutcome(t *testing.T) {
	var got []mcpserver.StepProgress
	obs := stepObserver{onStep: func(p mcpserver.StepProgress) { got = append(got, p) }}
	obs.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "lint", Status: domain.StepPass}})
	obs.OnStep(application.StepEvent{Step: "review", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "review", Status: domain.StepPass, Outcome: domain.StepSkipped, OutcomeReason: "no agent configured"}})
	if len(got) != 2 {
		t.Fatalf("forwarded %d steps", len(got))
	}
	if got[0].Outcome != "executed" || got[0].OutcomeReason != "" {
		t.Errorf("executed step: %+v", got[0])
	}
	if got[1].Status != "pass" || got[1].Outcome != "skipped" || got[1].OutcomeReason != "no agent configured" {
		t.Errorf("skipped step: %+v", got[1])
	}
}

func TestStepOutcomeMaps_SortedRows(t *testing.T) {
	rows := stepOutcomeMaps(map[domain.StepName]domain.OutcomeNote{
		"test":   {Outcome: domain.StepCached, Reason: "inputs unchanged"},
		"review": {Outcome: domain.StepSkipped, Reason: "no agent configured"},
	})
	want := []any{
		map[string]any{"step": "review", "outcome": "skipped", "reason": "no agent configured"},
		map[string]any{"step": "test", "outcome": "cached", "reason": "inputs unchanged"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v", rows)
	}
	if rows := stepOutcomeMaps(nil); len(rows) != 0 {
		t.Errorf("no notes must render no rows, got %+v", rows)
	}
}

func TestPredictionReporter_LabelsSkipped(t *testing.T) {
	var out bytes.Buffer
	r := &predictionReporter{ctx: context.Background(), w: &out}
	r.OnStep(application.StepEvent{Phase: application.StepFinished, Result: domain.StepResult{
		Status: domain.StepPass, Outcome: domain.StepSkipped, Summary: "review: skipped (no agent configured)"}})
	if !strings.Contains(out.String(), "pass (skipped): review: skipped") {
		t.Errorf("output: %q", out.String())
	}
}
