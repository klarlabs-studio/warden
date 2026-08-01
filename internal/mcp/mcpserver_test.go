package mcpserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

	// A run happens on its own goroutine now, so runHook is written there and
	// read by the test — mu guards the crossing.
	mu      sync.Mutex
	run     RunSummary
	runErr  error
	runHook domain.Hook
	// progress is replayed to the onStep callback before the run finishes.
	progress []StepProgress
	// release, when set, holds the run open until the test closes it — the only
	// way to observe the running phase deterministically.
	release chan struct{}

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
	f.mu.Lock()
	f.runHook = hook
	f.mu.Unlock()
	return f.run, f.runErr
}

func (f *fakeFacade) RunTriggerStreaming(_ context.Context, hook domain.Hook, onStep func(StepProgress)) (RunSummary, error) {
	f.mu.Lock()
	f.runHook = hook
	f.mu.Unlock()
	// Hold the run open when a test wants to observe the running phase before
	// letting it finish.
	if f.release != nil {
		<-f.release
	}
	for _, s := range f.progress {
		onStep(s)
	}
	return f.run, f.runErr
}

// hookRan reports the hook the fake was asked to run, under the lock: the run is
// on its own goroutine, so a plain field read would race.
func (f *fakeFacade) hookRan() domain.Hook {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runHook
}

// waitForRun polls until the run leaves the running phase. Runs are asynchronous
// now, so every assertion about an outcome has to wait for one.
func waitForRun(t *testing.T, runs *registry, id string) RunStatusOutput {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := handleRunStatus(runs, RunStatusInput{RunID: id})
		if err != nil {
			t.Fatalf("run_status: %v", err)
		}
		if got.Phase != RunRunning {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never left the running phase", id)
		}
		time.Sleep(time.Millisecond)
	}
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

// run_trigger now STARTS a run and returns a handle; the verdict arrives via
// run_status. These tests therefore assert the handle, then wait.
func TestHandleRunTrigger_StartsAndReportsThroughStatus(t *testing.T) {
	want := RunSummary{
		Outcome:  "passed",
		Hook:     "pre-push",
		Steps:    []domain.StepName{domain.StepLint},
		Findings: []domain.Finding{{Severity: domain.SeverityLow, Message: "nit"}},
		Message:  "all green",
		RunID:    "run-42",
	}
	f := &fakeFacade{run: want}
	runs := newRegistry()

	started, err := handleRunTrigger(f, AllowAllRuns, runs, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if started.RunID == "" {
		t.Fatal("run_trigger must return a run id to poll")
	}

	got := waitForRun(t, runs, started.RunID)
	if got.Phase != RunComplete {
		t.Fatalf("phase = %q, want complete", got.Phase)
	}
	if got.Summary == nil || got.Summary.Outcome != "passed" || len(got.Summary.Findings) != 1 {
		t.Errorf("summary mismatch: %+v", got.Summary)
	}
	if f.hookRan() != domain.PrePush {
		t.Errorf("hook not forwarded: got %q", f.hookRan())
	}
}

// The whole point of the change: run_trigger must return BEFORE the pipeline
// finishes. A five-minute test step was a five-minute silent tool call, and most
// MCP clients time out long before it returned.
func TestHandleRunTrigger_ReturnsWhileTheRunIsStillGoing(t *testing.T) {
	release := make(chan struct{})
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}, release: release}
	runs := newRegistry()

	started, err := handleRunTrigger(f, AllowAllRuns, runs, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if started.Phase != RunRunning {
		t.Errorf("phase = %q, want running — the call must not block on the pipeline", started.Phase)
	}
	if started.Summary != nil {
		t.Error("a run still in flight must not report a summary")
	}

	close(release)
	if got := waitForRun(t, runs, started.RunID); got.Phase != RunComplete {
		t.Errorf("phase after release = %q, want complete", got.Phase)
	}
}

// Steps are reported as they finish, which is strictly more than the old
// synchronous call ever gave: an agent can act on a lint failure while the test
// step is still running.
func TestHandleRunStatus_ReportsFinishedStepsWithFindings(t *testing.T) {
	f := &fakeFacade{
		run: RunSummary{Outcome: "failed"},
		progress: []StepProgress{
			{Step: domain.StepLint, Status: "pass", Summary: "lint passed"},
			{Step: domain.StepTest, Status: "fail", Findings: []domain.Finding{
				{Severity: domain.SeverityHigh, Message: "boom"},
			}},
		},
	}
	runs := newRegistry()
	started, err := handleRunTrigger(f, AllowAllRuns, runs, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := waitForRun(t, runs, started.RunID)
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(got.Steps), got.Steps)
	}
	if got.Steps[0].Step != domain.StepLint || got.Steps[0].Status != "pass" {
		t.Errorf("first step mismatch: %+v", got.Steps[0])
	}
	if len(got.Steps[1].Findings) != 1 || got.Steps[1].Findings[0].Message != "boom" {
		t.Errorf("a failed step must carry its findings: %+v", got.Steps[1])
	}
}

// A run that could not be carried out is `errored`, distinct from a run that
// completed with a failing verdict. Collapsing the two would tell an agent its
// code was rejected when the pipeline never ran.
func TestHandleRunStatus_PipelineErrorIsNotAFailedGate(t *testing.T) {
	sentinel := errors.New("pipeline exploded")
	runs := newRegistry()
	started, err := handleRunTrigger(&fakeFacade{runErr: sentinel}, AllowAllRuns, runs, RunTriggerInput{Hook: "pre-commit"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := waitForRun(t, runs, started.RunID)
	if got.Phase != RunErrored {
		t.Errorf("phase = %q, want errored", got.Phase)
	}
	if got.Summary != nil {
		t.Error("an errored run has no verdict to report")
	}
	if got.Error == "" {
		t.Error("an errored run must say what went wrong")
	}
}

// Polling an id that does not exist must be an error, not an empty status: a
// caller who mistyped would otherwise poll forever against silence.
func TestHandleRunStatus_UnknownRun(t *testing.T) {
	if _, err := handleRunStatus(newRegistry(), RunStatusInput{RunID: "run-999"}); err == nil {
		t.Fatal("expected an error for an unknown run id")
	}
	if _, err := handleRunStatus(newRegistry(), RunStatusInput{}); err == nil {
		t.Fatal("expected an error when run_id is missing")
	}
}

func TestHandleRunTrigger_BadHook(t *testing.T) {
	_, err := handleRunTrigger(&fakeFacade{}, AllowAllRuns, newRegistry(), RunTriggerInput{Hook: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown hook, got nil")
	}
}

// The core trust guard: when the gate denies, run_trigger returns the gate's
// error and never starts a run, so a possibly-untrusted repo's commands are not
// executed on the auto-approved path.
func TestHandleRunTrigger_GateRefuses(t *testing.T) {
	sentinel := errors.New("not trusted")
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	deny := func() error { return sentinel }

	_, err := handleRunTrigger(f, deny, newRegistry(), RunTriggerInput{Hook: "pre-push"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected gate refusal to propagate, got %v", err)
	}
	if f.hookRan() != "" {
		t.Errorf("facade must not run when the gate refuses; ran hook %q", f.hookRan())
	}
}

func TestHandleRunTrigger_GatePermits(t *testing.T) {
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	runs := newRegistry()
	started, err := handleRunTrigger(f, AllowAllRuns, runs, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := waitForRun(t, runs, started.RunID)
	if got.Summary == nil || got.Summary.Outcome != "passed" || f.hookRan() != domain.PrePush {
		t.Errorf("permitted run did not reach facade: %+v hook=%q", got.Summary, f.hookRan())
	}
}

func TestHandleRunTrigger_NilGateUnguarded(t *testing.T) {
	f := &fakeFacade{run: RunSummary{Outcome: "passed"}}
	runs := newRegistry()
	started, err := handleRunTrigger(f, nil, runs, RunTriggerInput{Hook: "pre-push"})
	if err != nil {
		t.Fatalf("nil gate should not block: %v", err)
	}
	waitForRun(t, runs, started.RunID)
	if f.hookRan() != domain.PrePush {
		t.Errorf("nil gate should reach facade, got hook %q", f.hookRan())
	}
}

func TestErrNotSupported(t *testing.T) {
	if err := errNotSupported("run_status"); err == nil {
		t.Fatal("expected a non-nil not-supported error")
	}
}
