package application

import (
	"context"
	"errors"
	"slices"
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

// An approver's notice about how it answered reaches the run's warnings
// whenever a push was attempted on that answer, and stays out of a run the
// gate stopped, where nothing was approved for it to qualify.
func TestRunner_ApproverNoticeReachesTheWarnings(t *testing.T) {
	const notice = "approved by an agent, not a person"
	cases := []struct {
		name        string
		decision    Decision
		failPush    bool
		wantOutcome domain.Outcome
		wantNotice  bool
	}{
		{"approved and pushed", Decision{Approved: true, Principal: "agent", Notice: notice}, false, domain.OutcomePassed, true},
		{"approved, push failed", Decision{Approved: true, Principal: "agent", Notice: notice}, true, domain.OutcomeAborted, true},
		{"needs a human", Decision{NeedsHuman: true, Principal: "agent", Rationale: "a person must approve", Notice: notice}, false, domain.OutcomeNeedsApproval, false},
		{"declined", Decision{Principal: "agent", Notice: notice}, false, domain.OutcomeRejected, false},
		{"no notice", Decision{Approved: true, Principal: "agent"}, false, domain.OutcomePassed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
			kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
			if c.failPush {
				kernel.approveErr = errors.New("remote rejected")
			}
			r := newRunner(t, git, kernel, &recordingApprover{decision: c.decision}, approvalCfg())

			res, err := r.Run(context.Background(), domain.PrePush)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != c.wantOutcome {
				t.Fatalf("outcome = %s, want %s (%s)", res.Outcome, c.wantOutcome, res.Message)
			}
			if got := slices.Contains(res.Warnings, notice); got != c.wantNotice {
				t.Errorf("notice in warnings = %v, want %v: %q", got, c.wantNotice, res.Warnings)
			}
		})
	}
}
