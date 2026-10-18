package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// guardPolicy is a resolved policy under profile (or none, for ""), running
// steps sequentially or in one parallel batch.
func guardPolicy(profile string, parallel bool, steps ...domain.StepName) domain.ResolvedPolicy {
	p := domain.ResolvedPolicy{Hook: domain.PrePush, Steps: steps, Parallel: parallel}
	if profile != "" {
		p.Profile = &domain.ResolvedProfile{Name: profile}
	}
	return p
}

// guardRun is a run plus the results its steps finished with, as the runner
// reported them to its observer.
type guardRun struct {
	*domain.Run
	results map[domain.StepName]domain.StepResult
}

type recordingObserver struct {
	finished map[domain.StepName]domain.StepResult
}

func (o recordingObserver) OnStep(e StepEvent) {
	if e.Phase == StepFinished {
		o.finished[e.Step] = e.Result
	}
}

func runGuardBatch(t *testing.T, policy domain.ResolvedPolicy, wt *fakeWorktree, kernel *fakeKernel) guardRun {
	t.Helper()
	run := domain.NewRun("guard-test", domain.PrePush, policy, "main")
	reg := newWorktreeRegistry()
	obs := recordingObserver{finished: map[domain.StepName]domain.StepResult{}}
	r := &Runner{Observer: obs}
	for _, batch := range policy.Batches() {
		if err := r.runBatch(context.Background(), run, kernel, batch, wt, reg, policy); err != nil {
			t.Fatal(err)
		}
		if run.IsTerminal() {
			break
		}
	}
	return guardRun{Run: run, results: obs.finished}
}

func resultFor(run guardRun, step domain.StepName) (domain.StepResult, bool) {
	r, ok := run.results[step]
	return r, ok
}

// Without a profile, and under permissive, nothing changes: a review running
// on its own runs in the validated worktree as it always has, and no tree
// state is captured.
func TestAgentGuard_UnprofiledAndPermissiveAreUnchanged(t *testing.T) {
	for _, profile := range []string{"", "permissive@1"} {
		wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
		run := runGuardBatch(t, guardPolicy(profile, false, domain.StepReview), wt, &fakeKernel{})
		if len(wt.clones) != 0 || wt.treeStates != 0 {
			t.Errorf("profile %q: clones=%d treeStates=%d, want none", profile, len(wt.clones), wt.treeStates)
		}
		if r, _ := resultFor(run, domain.StepReview); r.AgentBoundary != nil || r.Status != domain.StepPass {
			t.Errorf("profile %q: result changed: %+v", profile, r)
		}
	}
}

// Under standard, a review that runs on its own (the batch path that used to
// hand it the validated worktree) runs in a clone, which is removed after.
func TestAgentGuard_SingletonAgentStepRunsInAClone(t *testing.T) {
	wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
	run := runGuardBatch(t, guardPolicy("standard@1", false, domain.StepReview, domain.StepTest), wt, &fakeKernel{})
	if len(wt.clones) != 1 || !wt.clones[0].removed {
		t.Fatalf("review must run in a removed clone: %+v", wt.clones)
	}
	r, _ := resultFor(run, domain.StepReview)
	if r.Status != domain.StepPass || r.AgentBoundary == nil || !r.AgentBoundary.Isolated || !r.AgentBoundary.TreeUnchanged {
		t.Fatalf("observation missing or wrong: %+v", r)
	}
	if tr, _ := resultFor(run, domain.StepTest); tr.AgentBoundary != nil {
		t.Errorf("a shell step is not an agent step: %+v", tr)
	}
}

// A clone that cannot be made fails the step; it does not fall back to the
// validated worktree, and the agent never runs.
func TestAgentGuard_CloneFailureFailsTheStep(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		wt := &fakeWorktree{dir: "/wt", headSHA: "sha1", cloneErr: errors.New("disk full")}
		kernel := &fakeKernel{}
		run := runGuardBatch(t, guardPolicy("strict@1", parallel, domain.StepReview, domain.StepTest), wt, kernel)
		r, ok := resultFor(run, domain.StepReview)
		if !ok || r.Status != domain.StepFail || !strings.Contains(r.Findings[0].Message, "disk full") || !strings.Contains(r.Findings[0].Message, "strict@1") {
			t.Fatalf("parallel=%v: want a failed review naming the cause and profile, got %+v", parallel, r)
		}
		for _, s := range kernel.ran {
			if s == domain.StepReview {
				t.Fatalf("parallel=%v: review ran without its clone", parallel)
			}
		}
	}
}

// An agent that commits in its clone (an amend moves HEAD) or edits a tracked
// file fails the step, and says what it changed.
func TestAgentGuard_AgentChangingItsCloneFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fakeWorktree)
		want   string
	}{
		{"amend", func(c *fakeWorktree) { c.headSHA = "amended" }, "moved HEAD"},
		{"edit", func(c *fakeWorktree) { c.diffSince = "diff --git a/x b/x" }, "changed tracked files"},
		{"stage", func(c *fakeWorktree) { c.index = "other-tree" }, "changed the index"},
	} {
		for _, parallel := range []bool{false, true} {
			wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
			kernel := &fakeKernel{during: func(s domain.StepName) {
				if s == domain.StepReview {
					tc.mutate(wt.clones[0])
				}
			}}
			run := runGuardBatch(t, guardPolicy("standard@1", parallel, domain.StepReview, domain.StepTest), wt, kernel)
			r, _ := resultFor(run, domain.StepReview)
			if r.Status != domain.StepFail || r.AgentBoundary == nil || r.AgentBoundary.TreeUnchanged || !r.AgentBoundary.Isolated {
				t.Fatalf("%s parallel=%v: want a failed, isolated, changed review: %+v", tc.name, parallel, r)
			}
			if !strings.Contains(r.Summary, tc.want) {
				t.Errorf("%s parallel=%v: summary %q does not say %q", tc.name, parallel, r.Summary, tc.want)
			}
			if run.Outcome() != domain.OutcomeFailed {
				t.Errorf("%s parallel=%v: run outcome %s", tc.name, parallel, run.Outcome())
			}
			if wt.headSHA != "sha1" {
				t.Errorf("%s: the validated worktree moved", tc.name)
			}
		}
	}
}

// The validated worktree is the one whose HEAD gets attested. If it changes
// while an isolated agent step runs, warden cannot rule the agent out, so the
// agent step fails and is not reported tree-unchanged.
func TestAgentGuard_ValidatedWorktreeChangeFailsTheAgentStep(t *testing.T) {
	wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
	kernel := &fakeKernel{during: func(s domain.StepName) {
		if s == domain.StepReview {
			wt.mu.Lock()
			wt.headSHA = "rewritten"
			wt.mu.Unlock()
		}
	}}
	run := runGuardBatch(t, guardPolicy("standard@1", true, domain.StepReview, domain.StepTest), wt, kernel)
	r, _ := resultFor(run, domain.StepReview)
	if r.Status != domain.StepFail || r.AgentBoundary == nil || r.AgentBoundary.TreeUnchanged {
		t.Fatalf("a change to the validated worktree must fail the agent step: %+v", r)
	}
	if !strings.Contains(r.Summary, "validated worktree moved HEAD") {
		t.Errorf("summary must name the validated worktree: %q", r.Summary)
	}
}

// A step whose writes standard lets the config keep (writes:) is not
// isolated: isolating it would silently discard writes the config authorized.
func TestAgentGuard_KeptWritesAreNotIsolated(t *testing.T) {
	p := guardPolicy("standard@1", false, domain.StepReview)
	p.WriteSteps = map[domain.StepName]bool{domain.StepReview: true}
	if b := p.AgentBoundaryFor(domain.StepReview); b.Isolate || !b.RequireExecution {
		t.Fatalf("kept-writes agent step: %+v", b)
	}
	wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
	runGuardBatch(t, p, wt, &fakeKernel{})
	if len(wt.clones) != 0 {
		t.Fatalf("kept writes were isolated: %d clones", len(wt.clones))
	}
}
