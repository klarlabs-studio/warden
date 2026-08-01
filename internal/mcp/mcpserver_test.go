package mcpserver

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// fakeFacade is a scriptable Facade double: each field controls one method's
// return, and the *Calls fields record what the handlers passed through so we
// can assert on translation, not just the happy-path value.
type fakeFacade struct {
	policy    domain.ResolvedPolicy
	policyErr error
	policyArg struct {
		hook   domain.Hook
		branch string
		paths  []string
	}

	preCommit []domain.StepName
	prePush   []domain.StepName
	stepsErr  error

	run     RunSummary
	runErr  error
	runHook domain.Hook

	// The read-only provenance surface. The *Arg fields record what each handler
	// passed through, so a test can assert on the translation the handler does
	// (defaulting to HEAD, resolving the roster from the base) rather than only
	// on the value that comes back.
	verify    ProvenanceRecord
	verifyErr error
	verifyArg struct {
		commit string
		keys   []string
	}

	rangeVerify    RangeVerifyOutput
	rangeVerifyErr error
	rangeVerifyArg struct {
		base, head string
		opts       RangeVerifyRequest
	}

	doctor       domain.AuditReport
	doctorErr    error
	doctorBranch string

	audit       domain.AuditReport
	auditErr    error
	auditBranch string

	status    StatusOutput
	statusErr error
}

func (f *fakeFacade) PolicyExplain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error) {
	f.policyArg.hook = hook
	f.policyArg.branch = branch
	f.policyArg.paths = paths
	return f.policy, f.policyErr
}

func (f *fakeFacade) StepsList() ([]domain.StepName, []domain.StepName, error) {
	return f.preCommit, f.prePush, f.stepsErr
}

func (f *fakeFacade) RunTrigger(_ context.Context, hook domain.Hook) (RunSummary, error) {
	f.runHook = hook
	return f.run, f.runErr
}

func (f *fakeFacade) Verify(commitish string, trustedKeys []string) (ProvenanceRecord, error) {
	f.verifyArg.commit = commitish
	f.verifyArg.keys = trustedKeys
	return f.verify, f.verifyErr
}

func (f *fakeFacade) VerifyRange(base, head string, opts RangeVerifyRequest) (RangeVerifyOutput, error) {
	f.rangeVerifyArg.base = base
	f.rangeVerifyArg.head = head
	f.rangeVerifyArg.opts = opts
	return f.rangeVerify, f.rangeVerifyErr
}

func (f *fakeFacade) Doctor(branch string) (domain.AuditReport, error) {
	f.doctorBranch = branch
	return f.doctor, f.doctorErr
}

func (f *fakeFacade) Audit(branch string) (domain.AuditReport, error) {
	f.auditBranch = branch
	return f.audit, f.auditErr
}

func (f *fakeFacade) Status() (StatusOutput, error) { return f.status, f.statusErr }

func TestNewServer_BuildsWithoutPanic(t *testing.T) {
	srv := NewServer(&fakeFacade{}, "1.2.3", AllowAllRuns)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	// Every documented tool must be registered, including the not-supported stubs.
	for _, name := range []string{"policy_explain", "steps_list", "run_trigger", "run_respond", "run_status"} {
		if _, ok := srv.GetTool(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestHandlePolicyExplain(t *testing.T) {
	want := domain.ResolvedPolicy{
		Hook:            domain.PrePush,
		Steps:           []domain.StepName{domain.StepLint, domain.StepTest},
		Risk:            domain.RiskHigh,
		RequireApproval: true,
		MatchedRules:    []string{"default", "risky"},
	}
	f := &fakeFacade{policy: want}

	got, err := handlePolicyExplain(f, PolicyExplainInput{
		Hook:   "pre_push",
		Branch: "main",
		Paths:  []string{"cmd/main.go"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Risk != want.Risk || got.RequireApproval != want.RequireApproval {
		t.Errorf("policy mismatch: got %+v want %+v", got, want)
	}
	// The snake_case hook form must normalise to the canonical Hook.
	if f.policyArg.hook != domain.PrePush {
		t.Errorf("hook not parsed: got %q", f.policyArg.hook)
	}
	if f.policyArg.branch != "main" || len(f.policyArg.paths) != 1 {
		t.Errorf("branch/paths not forwarded: %+v", f.policyArg)
	}
}

func TestHandlePolicyExplain_BadHook(t *testing.T) {
	_, err := handlePolicyExplain(&fakeFacade{}, PolicyExplainInput{Hook: "post-merge"})
	if err == nil {
		t.Fatal("expected error for unknown hook, got nil")
	}
}

func TestHandlePolicyExplain_FacadeError(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := handlePolicyExplain(&fakeFacade{policyErr: sentinel}, PolicyExplainInput{Hook: "pre-commit"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected facade error to propagate, got %v", err)
	}
}

func TestHandleStepsList(t *testing.T) {
	f := &fakeFacade{
		preCommit: []domain.StepName{domain.StepLint},
		prePush:   []domain.StepName{domain.StepIntent, domain.StepTest},
	}
	out, err := handleStepsList(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.PreCommit) != 1 || out.PreCommit[0] != domain.StepLint {
		t.Errorf("pre_commit mismatch: %+v", out.PreCommit)
	}
	if len(out.PrePush) != 2 || out.PrePush[1] != domain.StepTest {
		t.Errorf("pre_push mismatch: %+v", out.PrePush)
	}
}

func TestHandleStepsList_Error(t *testing.T) {
	sentinel := errors.New("cfg broken")
	_, err := handleStepsList(&fakeFacade{stepsErr: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected steps error to propagate, got %v", err)
	}
}

func TestHandleRunTrigger(t *testing.T) {
	want := RunSummary{
		Outcome:  "passed",
		Hook:     "pre-push",
		Steps:    []domain.StepName{domain.StepLint},
		Findings: []domain.Finding{{Severity: domain.SeverityLow, Message: "nit"}},
		Message:  "all green",
		RunID:    "run-42",
	}
	f := &fakeFacade{run: want}

	got, err := handleRunTrigger(context.Background(), f, AllowAllRuns, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Outcome != "passed" || got.RunID != "run-42" || len(got.Findings) != 1 {
		t.Errorf("summary mismatch: got %+v", got)
	}
	if f.runHook != domain.PrePush {
		t.Errorf("hook not forwarded: got %q", f.runHook)
	}
}

func TestHandleRunTrigger_BadHook(t *testing.T) {
	_, err := handleRunTrigger(context.Background(), &fakeFacade{}, AllowAllRuns, RunTriggerInput{Hook: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown hook, got nil")
	}
}

func TestHandleRunTrigger_FacadeError(t *testing.T) {
	sentinel := errors.New("pipeline exploded")
	_, err := handleRunTrigger(context.Background(), &fakeFacade{runErr: sentinel}, AllowAllRuns, RunTriggerInput{Hook: "pre-commit"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected run error to propagate, got %v", err)
	}
}

// TestHandleRunTrigger_GateRefuses is the core trust guard: when the gate
// denies, run_trigger returns the gate's error and never touches the facade, so
// a possibly-untrusted repo's commands are not executed on the auto-approved
// MCP/axi path.
func TestHandleRunTrigger_GateRefuses(t *testing.T) {
	sentinel := errors.New("not trusted")
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	deny := func() error { return sentinel }

	_, err := handleRunTrigger(context.Background(), f, deny, RunTriggerInput{Hook: "pre-push"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected gate refusal to propagate, got %v", err)
	}
	if f.runHook != "" {
		t.Errorf("facade must not run when the gate refuses; ran hook %q", f.runHook)
	}
}

// TestHandleRunTrigger_GatePermits confirms a permitting gate lets the run
// proceed to the facade unchanged.
func TestHandleRunTrigger_GatePermits(t *testing.T) {
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	got, err := handleRunTrigger(context.Background(), f, AllowAllRuns, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Outcome != "passed" || f.runHook != domain.PrePush {
		t.Errorf("permitted run did not reach facade: got %+v hook=%q", got, f.runHook)
	}
}

// TestHandleRunTrigger_NilGateUnguarded documents that a nil gate leaves
// run_trigger unguarded, so embedders must opt in deliberately.
func TestHandleRunTrigger_NilGateUnguarded(t *testing.T) {
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	if _, err := handleRunTrigger(context.Background(), f, nil, RunTriggerInput{Hook: "pre-push"}); err != nil {
		t.Fatalf("nil gate should not block: %v", err)
	}
	if f.runHook != domain.PrePush {
		t.Errorf("nil gate should reach facade, got hook %q", f.runHook)
	}
}

func TestErrNotSupported(t *testing.T) {
	if err := errNotSupported("run_status"); err == nil {
		t.Fatal("expected a non-nil not-supported error")
	}
}
