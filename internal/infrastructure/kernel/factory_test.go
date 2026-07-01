package kernel

import (
	"context"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// fakeStep is a scripted application.Step for exercising the axi wiring without
// running real lint/agent processes.
type fakeStep struct {
	name     domain.StepName
	status   domain.StepStatus
	findings []domain.Finding
	ran      bool
}

func (s *fakeStep) Name() domain.StepName { return s.name }

func (s *fakeStep) Run(_ context.Context, _ application.StepContext) (domain.StepResult, error) {
	s.ran = true
	return domain.StepResult{Step: s.name, Status: s.status, Findings: s.findings, Summary: "ran"}, nil
}

func newFactory(steps ...*fakeStep) *Factory {
	reg := application.Registry{}
	for _, s := range steps {
		reg[s.name] = s
	}
	return NewFactory(reg)
}

func policyFor(steps ...domain.StepName) domain.ResolvedPolicy {
	return domain.ResolvedPolicy{Hook: domain.PrePush, Steps: steps, Agents: map[domain.StepName]string{}, AutoFix: map[domain.StepName]int{}}
}

func TestKernel_StepResultRoundTripsAndRecordsEvidence(t *testing.T) {
	lint := &fakeStep{name: domain.StepLint, status: domain.StepPass,
		findings: []domain.Finding{{Severity: domain.SeverityLow, Message: "nit"}}}
	k, err := newFactory(lint).New(policyFor(domain.StepLint), application.StepContext{}, new([]domain.Finding), nil)
	if err != nil {
		t.Fatal(err)
	}

	out, err := k.Execute(context.Background(), domain.StepLint)
	if err != nil {
		t.Fatal(err)
	}
	if !lint.ran {
		t.Fatal("step executor was not invoked")
	}
	if out.Result.Status != domain.StepPass || out.Result.Step != domain.StepLint {
		t.Errorf("result did not round-trip through the kernel: %+v", out.Result)
	}
	if len(out.Result.Findings) != 1 || out.Result.Findings[0].Message != "nit" {
		t.Errorf("findings lost through the kernel: %+v", out.Result.Findings)
	}

	// Evidence: one step-summary record plus one per finding, chained.
	root, entries, err := k.Finalize()
	if err != nil {
		t.Fatalf("evidence chain must verify: %v", err)
	}
	if root == "" || len(entries) < 2 {
		t.Errorf("expected a verified chain with step + finding evidence, got root=%q n=%d", root, len(entries))
	}
}

func TestKernel_PushActionPausesForApprovalThenRunsPushFunc(t *testing.T) {
	pushed := false
	push := func(context.Context) (domain.StepResult, error) {
		pushed = true
		return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
	}
	k, err := newFactory().New(policyFor(), application.StepContext{}, new([]domain.Finding), push)
	if err != nil {
		t.Fatal(err)
	}

	// The write-external push action must pause before its executor runs.
	gate, err := k.Execute(context.Background(), domain.StepPush)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.NeedsApproval {
		t.Fatal("push action must pause at awaiting_approval")
	}
	if pushed {
		t.Fatal("push must not run before approval")
	}

	if _, err := k.Approve(context.Background(), gate.SessionID, "tester", "ok"); err != nil {
		t.Fatal(err)
	}
	if !pushed {
		t.Error("approving the gate must run the push func")
	}
}

func TestKernel_PushFuncErrorSurfacesAsError(t *testing.T) {
	push := func(context.Context) (domain.StepResult, error) {
		return domain.StepResult{}, context.DeadlineExceeded
	}
	k, err := newFactory().New(policyFor(), application.StepContext{}, new([]domain.Finding), push)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := k.Execute(context.Background(), domain.StepPush)
	if err != nil {
		t.Fatal(err)
	}
	// A failing push executor must surface as an error from Approve, not an
	// in-band success — this is the bug the runner relies on being caught.
	if _, err := k.Approve(context.Background(), gate.SessionID, "tester", "ok"); err == nil {
		t.Error("a failing push executor must surface an error from Approve")
	}
}

func TestKernel_CustomStepMissingBinaryErrorsAtBuild(t *testing.T) {
	// A custom (non-built-in) step with no binary on PATH must fail fast at
	// build, not silently skip the gate.
	_, err := newFactory().New(policyFor("no-such-custom-step"), application.StepContext{}, new([]domain.Finding), nil)
	if err == nil {
		t.Error("expected build to fail for an unresolvable custom step")
	}
}
