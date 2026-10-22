package application

import (
	"context"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// recordingApprover returns a fixed decision and keeps the request it saw.
type recordingApprover struct {
	decision Decision
	got      *ApprovalRequest
}

func (a *recordingApprover) Approve(_ context.Context, req ApprovalRequest) (Decision, error) {
	a.got = &req
	return a.decision, nil
}

// An approver that may not satisfy the gate ends the run needs_approval: not
// rejected (nobody declined), nothing pushed, no record, and the approver's
// explanation is the run's message.
func TestRunner_ApproverNeedingAHumanEndsNeedsApproval(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	approver := &recordingApprover{decision: Decision{NeedsHuman: true, Principal: "agent", Rationale: "a person must approve"}}
	r := newRunner(t, git, kernel, approver, approvalCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeNeedsApproval {
		t.Fatalf("outcome = %s, want needs_approval", res.Outcome)
	}
	if res.Message != "a person must approve" {
		t.Errorf("message = %q, want the approver's rationale", res.Message)
	}
	if git.pushed || kernel.approved || res.PushPerformed {
		t.Error("a gate nobody approved must not push")
	}
	if res.Record != nil {
		t.Error("a gate nobody approved must not produce a record")
	}
}

// NeedsHuman wins over a stray Approved: an approver that says it did not
// decide has not approved, whatever else it set.
func TestRunner_NeedsHumanIsNeverAnApproval(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	approver := &recordingApprover{decision: Decision{Approved: true, NeedsHuman: true}}
	r := newRunner(t, git, kernel, approver, approvalCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeNeedsApproval || git.pushed {
		t.Fatalf("outcome = %s pushed = %v, want needs_approval and no push", res.Outcome, git.pushed)
	}
}

// The approver sees the profile the run's policy was resolved under, so an
// agent-surface approver can tell a profiled repo from an unprofiled one.
func TestRunner_ApprovalRequestCarriesTheProfile(t *testing.T) {
	for _, profile := range []string{"", "permissive"} {
		t.Run("profile="+profile, func(t *testing.T) {
			git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
			kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
			approver := &recordingApprover{decision: Decision{Approved: true, Principal: "test"}}
			cfg := approvalCfg()
			cfg.Profile = profile
			r := newRunner(t, git, kernel, approver, cfg)

			res, err := r.Run(context.Background(), domain.PrePush)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != domain.OutcomePassed {
				t.Fatalf("outcome = %s, want passed", res.Outcome)
			}
			if approver.got == nil {
				t.Fatal("a rule requiring approval must consult the approver")
			}
			switch {
			case profile == "" && approver.got.Profile != nil:
				t.Errorf("an unprofiled run must carry no profile, got %+v", approver.got.Profile)
			case profile != "" && (approver.got.Profile == nil || approver.got.Profile.Name != "permissive@1"):
				t.Errorf("profile = %+v, want permissive@1", approver.got.Profile)
			}
		})
	}
}
