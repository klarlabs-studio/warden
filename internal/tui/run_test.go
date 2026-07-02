package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// fakeSvc drives the bridge like the real service would: it emits step events
// through the observer and returns a result. It satisfies tui.Runner.
type fakeSvc struct {
	obs   application.Observer
	steps []domain.StepName
	res   application.RunResult
}

func (f *fakeSvc) SetObserver(o application.Observer) { f.obs = o }
func (f *fakeSvc) Explain(domain.Hook, string, []string) (domain.ResolvedPolicy, error) {
	return domain.ResolvedPolicy{Steps: f.steps}, nil
}
func (f *fakeSvc) Run(context.Context, domain.Hook) (application.RunResult, error) {
	for _, s := range f.steps {
		f.obs.OnStep(application.StepEvent{Step: s, Phase: application.StepStarted})
		f.obs.OnStep(application.StepEvent{Step: s, Phase: application.StepFinished,
			Result: domain.StepResult{Step: s, Status: domain.StepPass}})
	}
	return f.res, nil
}

// TestRun_ProgramLoopDrivesToCompletion runs the real bubbletea program
// headlessly (injected input/output) and verifies the event loop consumes the
// runner's step events and quits on completion with the right result.
func TestRun_ProgramLoopDrivesToCompletion(t *testing.T) {
	steps := []domain.StepName{"lint", "test"}
	svc := &fakeSvc{
		steps: steps,
		res: application.RunResult{
			Outcome: domain.OutcomePassed, Hook: domain.PrePush,
			Message: "warden pushed the gated commit(s)",
		},
	}
	br := NewApprover()

	// A blocking input reader (never written) keeps bubbletea from EOF-quitting
	// before the run finishes; output is captured for assertion.
	pr, pw := io.Pipe()
	defer pw.Close()
	var out bytes.Buffer

	done := make(chan application.RunResult, 1)
	go func() {
		res, err := Run(svc, br, domain.PrePush, steps, nil,
			tea.WithInput(pr), tea.WithOutput(&out), tea.WithoutSignalHandler())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()

	select {
	case res := <-done:
		if res.Outcome != domain.OutcomePassed {
			t.Errorf("outcome = %s, want passed", res.Outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TUI program did not complete")
	}

	rendered := out.String()
	for _, want := range []string{"warden pre-push", "lint", "test", "passed"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output missing %q; got:\n%s", want, rendered)
		}
	}
}
