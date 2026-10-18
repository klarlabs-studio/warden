package application

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// channelApprover approves with a chosen channel, as the real approvers do.
type channelApprover struct{ channel domain.ApprovalChannel }

func (a channelApprover) Approve(context.Context, ApprovalRequest) (Decision, error) {
	return Decision{Approved: true, Principal: "test-approver", Channel: a.channel}, nil
}

// observedRunKernel scripts one step per outcome warden can observe.
func observedRunKernel() *fakeKernel {
	reuse := &domain.ExecutionReuse{Source: domain.PredictionPurpose, Key: "k", RecordedAt: time.Unix(0, 0).UTC()}
	return &fakeKernel{results: map[domain.StepName]domain.StepResult{
		domain.StepLint:         {Step: domain.StepLint, Status: domain.StepPass},
		domain.StepTest:         {Step: domain.StepTest, Status: domain.StepPass, Outcome: domain.StepCached, OutcomeReason: "inputs unchanged"},
		domain.StepSecurityScan: {Step: domain.StepSecurityScan, Status: domain.StepPass, Reuse: reuse},
		domain.StepReview:       {Step: domain.StepReview, Status: domain.StepPass, Agent: "claude"},
		domain.StepIntent:       {Step: domain.StepIntent, Status: domain.StepPass, Outcome: domain.StepSkipped, OutcomeReason: "no agent configured"},
	}}
}

func observedRunCfg(profile string) domain.Config {
	cfg := prePushCfg()
	cfg.Profile = profile
	cfg.Steps = map[string][]domain.StepName{"pre_push": {
		domain.StepLint, domain.StepTest, domain.StepSecurityScan, domain.StepReview, domain.StepIntent,
	}}
	return cfg
}

// Under a profile the signed record states what was observed of each step —
// and only that. A cached step is not "executed", a prediction is not a host
// cache hit, a skipped agent step names no agent, and no isolation claim is
// made because nothing isolates agent steps yet.
func TestRunner_ProfiledRecordCarriesSignedObservations(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	r := newRunner(t, git, observedRunKernel(), fakeApprover{approve: true}, observedRunCfg("permissive"))
	r.Signer = newFakeSigner(t)

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	rec := git.note
	p, err := domain.ParseComplianceProfile("permissive")
	if err != nil {
		t.Fatal(err)
	}
	wantProfile := domain.ProfileRef{Name: "permissive", Version: 1, Digest: p.Digest()}
	if rec.Profile == nil || *rec.Profile != wantProfile {
		t.Errorf("profile = %+v, want %+v", rec.Profile, wantProfile)
	}
	wantOutcomes := map[domain.StepName]domain.StepObservation{
		domain.StepLint:         {Outcome: domain.StepExecuted},
		domain.StepTest:         {Outcome: domain.StepCached},
		domain.StepSecurityScan: {Outcome: domain.StepPredicted},
		domain.StepReview:       {Outcome: domain.StepExecuted, Agent: "claude"},
		domain.StepIntent:       {Outcome: domain.StepSkipped},
	}
	if !reflect.DeepEqual(rec.StepOutcomes, wantOutcomes) {
		t.Errorf("step outcomes = %+v\nwant %+v", rec.StepOutcomes, wantOutcomes)
	}
	// No rule required approval, so no approver was consulted.
	if want := (domain.ApprovalRef{Required: false, Channel: domain.ApprovalNone}); rec.Approval == nil || *rec.Approval != want {
		t.Errorf("approval = %+v, want %+v", rec.Approval, want)
	}
	if !rec.VerifySignature() {
		t.Fatal("a profiled record must verify under the current struct")
	}
	payload, _ := rec.SigningPayload()
	for _, key := range []string{`"profile":`, `"step_outcomes":`, `"approval":`} {
		if !strings.Contains(string(payload), key) {
			t.Errorf("signed payload lacks %s: %s", key, payload)
		}
	}
	for _, overclaim := range []string{"isolated", "tree_unchanged"} {
		if strings.Contains(string(payload), overclaim) {
			t.Errorf("payload claims %q, which nothing observed: %s", overclaim, payload)
		}
	}

	// Each observation is covered by the signature.
	tampered := rec
	tampered.StepOutcomes = map[domain.StepName]domain.StepObservation{}
	for k, v := range rec.StepOutcomes {
		tampered.StepOutcomes[k] = v
	}
	tampered.StepOutcomes[domain.StepIntent] = domain.StepObservation{Outcome: domain.StepExecuted}
	if tampered.VerifySignature() {
		t.Error("upgrading a skipped step to executed must break the signature")
	}
	tampered = rec
	tampered.Approval = &domain.ApprovalRef{Required: true, Channel: domain.ApprovalTTY}
	if tampered.VerifySignature() {
		t.Error("rewriting the approval must break the signature")
	}
	tampered = rec
	tampered.Profile = &domain.ProfileRef{Name: "strict", Version: 1, Digest: rec.Profile.Digest}
	if tampered.VerifySignature() {
		t.Error("relabelling the profile must break the signature")
	}
}

// Without a profile, the same run writes none of the new fields: the note is
// what it always was, so a pinned older verifier keeps accepting it.
func TestRunner_UnprofiledRecordCarriesNoObservations(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	r := newRunner(t, git, observedRunKernel(), channelApprover{channel: domain.ApprovalTTY}, observedRunCfg(""))
	r.Signer = newFakeSigner(t)

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	rec := git.note
	if rec.Profile != nil || rec.StepOutcomes != nil || rec.Approval != nil {
		t.Fatalf("an unprofiled record must carry no profile observations: %+v %+v %+v", rec.Profile, rec.StepOutcomes, rec.Approval)
	}
	payload, _ := rec.SigningPayload()
	for _, key := range []string{"profile", "step_outcomes", "approval", "predicted", "skipped"} {
		if strings.Contains(string(payload), key) {
			t.Errorf("unprofiled payload mentions %q: %s", key, payload)
		}
	}
}

// The approval record states the channel the approver reported, and treats an
// approver that names none as automatic: nothing says anyone was asked.
func TestRunner_ProfiledApprovalRecordsTheChannel(t *testing.T) {
	cases := []struct {
		name     string
		approver Approver
		want     domain.ApprovalRef
	}{
		{"tty", channelApprover{channel: domain.ApprovalTTY}, domain.ApprovalRef{Required: true, Channel: domain.ApprovalTTY, Principal: "test-approver"}},
		{"tui", channelApprover{channel: domain.ApprovalTUI}, domain.ApprovalRef{Required: true, Channel: domain.ApprovalTUI, Principal: "test-approver"}},
		{"auto", channelApprover{channel: domain.ApprovalAuto}, domain.ApprovalRef{Required: true, Channel: domain.ApprovalAuto, Principal: "test-approver"}},
		{"unnamed channel is auto", fakeApprover{approve: true}, domain.ApprovalRef{Required: true, Channel: domain.ApprovalAuto, Principal: "test"}},
		{"none is not a channel for a required approval", channelApprover{channel: domain.ApprovalNone}, domain.ApprovalRef{Required: true, Channel: domain.ApprovalAuto, Principal: "test-approver"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
			cfg := approvalCfg()
			cfg.Profile = "permissive"
			r := newRunner(t, git, &fakeKernel{}, tc.approver, cfg)
			if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
				t.Fatal(err)
			}
			if git.note.Approval == nil || *git.note.Approval != tc.want {
				t.Errorf("approval = %+v, want %+v", git.note.Approval, tc.want)
			}
		})
	}
}
