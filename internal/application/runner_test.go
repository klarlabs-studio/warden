package application

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// --- fakes ------------------------------------------------------------------

type fakeWorktree struct {
	dir       string
	headSHA   string
	removed   bool
	diffSince string
	mu        sync.Mutex
	clones    []*fakeWorktree // clones minted by Clone, for isolation assertions
}

func (w *fakeWorktree) Dir() string                { return w.dir }
func (w *fakeWorktree) HeadSHA() (string, error)   { return w.headSHA, nil }
func (w *fakeWorktree) DiffSince() (string, error) { return w.diffSince, nil }
func (w *fakeWorktree) Remove() error              { w.removed = true; return nil }
func (w *fakeWorktree) Clone(bool) (Worktree, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := &fakeWorktree{dir: fmt.Sprintf("%s-clone%d", w.dir, len(w.clones)), headSHA: w.headSHA}
	w.clones = append(w.clones, c)
	return c, nil
}

type fakeGit struct {
	root         string
	branch       string
	branchErr    error
	head         string
	pushed       bool
	notesPushed  bool
	wroteNote    bool
	note         domain.RunRecord
	ffErr        error
	mergeBaseErr error
	wt           *fakeWorktree
}

func (g *fakeGit) Root() string                     { return g.root }
func (g *fakeGit) CurrentBranch() (string, error)   { return g.branch, g.branchErr }
func (g *fakeGit) HeadSHA() (string, error)         { return g.head, nil }
func (g *fakeGit) MergeBase(string) (string, error) { return "base", g.mergeBaseErr }
func (g *fakeGit) DiffStats(string) (domain.DiffStats, error) {
	return domain.DiffStats{FilesTouched: 1, LinesChanged: 2}, nil
}
func (g *fakeGit) StagedDiffStats() (domain.DiffStats, error)  { return domain.DiffStats{}, nil }
func (g *fakeGit) SeedWorktreeFromHead(bool) (Worktree, error) { return g.wt, nil }
func (g *fakeGit) SeedWorktreeFromBranch(string, bool) (Worktree, error) {
	return g.wt, nil
}
func (g *fakeGit) FastForwardTo(_, _, _ string) error { return g.ffErr }
func (g *fakeGit) Push(string, string) error          { g.pushed = true; return nil }
func (g *fakeGit) WriteNote(_ string, rec domain.RunRecord) error {
	g.wroteNote = true
	g.note = rec
	return nil
}
func (g *fakeGit) PushNotes(string) error { g.notesPushed = true; return nil }

// fakeKernel scripts step outcomes and invokes the push closure on approval,
// mirroring how the real axi-backed kernel resolves the write-external gate.
type fakeKernel struct {
	outcomes map[domain.StepName]domain.StepStatus
	push     PushFunc
	approved bool
	execErr  error
}

func (k *fakeKernel) Execute(_ context.Context, step domain.StepName) (StepOutcome, error) {
	if k.execErr != nil && step != domain.StepPush {
		return StepOutcome{}, k.execErr
	}
	if step == domain.StepPush {
		return StepOutcome{NeedsApproval: true, SessionID: "s1"}, nil
	}
	status := k.outcomes[step]
	if status == "" {
		status = domain.StepPass
	}
	return StepOutcome{Result: domain.StepResult{Step: step, Status: status}}, nil
}

func (k *fakeKernel) ExecuteBatch(ctx context.Context, steps []domain.StepName, onFinish func(domain.StepName, StepOutcome)) ([]StepOutcome, error) {
	outcomes := make([]StepOutcome, 0, len(steps))
	for _, step := range steps {
		out, err := k.Execute(ctx, step)
		if err != nil {
			return nil, err
		}
		if onFinish != nil {
			onFinish(step, out)
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, nil
}

func (k *fakeKernel) Approve(ctx context.Context, _, _, _ string) (StepOutcome, error) {
	k.approved = true
	if _, err := k.push(ctx); err != nil {
		return StepOutcome{}, err
	}
	return StepOutcome{Result: domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}}, nil
}

func (k *fakeKernel) Reject(context.Context, string, string, string) (StepOutcome, error) {
	return StepOutcome{}, nil
}

func (k *fakeKernel) Finalize() (string, []domain.EvidenceEntry, error) {
	return "root", []domain.EvidenceEntry{{Kind: "x", Hash: "root"}}, nil
}

type fakeFactory struct{ kernel *fakeKernel }

func (f *fakeFactory) New(_ domain.ResolvedPolicy, _ StepContext, _ *[]domain.Finding, push PushFunc) (Kernel, error) {
	f.kernel.push = push
	return f.kernel, nil
}

type fakeApprover struct{ approve bool }

func (a fakeApprover) Approve(context.Context, ApprovalRequest) (Decision, error) {
	return Decision{Approved: a.approve, Principal: "test"}, nil
}

// fakeForge records EnsurePR/Comment calls and returns a scripted PR.
type fakeForge struct {
	available    bool
	called       bool
	pr           domain.PRInfo
	err          error
	commentBody  string
	commentCount int
}

func (f *fakeForge) Available() bool { return f.available }
func (f *fakeForge) EnsurePR(context.Context, string, string) (domain.PRInfo, error) {
	f.called = true
	return f.pr, f.err
}
func (f *fakeForge) Comment(_ context.Context, _, body string) error {
	f.commentCount++
	f.commentBody = body
	return nil
}
func (f *fakeForge) Checks(context.Context, string) (domain.CIStatus, error) {
	return domain.CIStatus{}, nil
}

// fakeConfigs is an in-memory ConfigRepository, so the runner test depends on
// no filesystem or YAML parser.
type fakeConfigs struct {
	cfg domain.Config
	err error
}

func (f fakeConfigs) Load() (domain.Config, error) { return f.cfg, f.err }

// --- helpers ----------------------------------------------------------------

func newRunner(t *testing.T, git *fakeGit, kernel *fakeKernel, approver Approver, cfg domain.Config) *Runner {
	t.Helper()
	return &Runner{
		Git:      git,
		Configs:  fakeConfigs{cfg: cfg},
		Kernels:  &fakeFactory{kernel: kernel},
		Approver: approver,
		Settings: Settings{Version: "test", Remote: "origin"},
		Now:      func() time.Time { return time.Unix(0, 0).UTC() },
		NewID:    func() string { return "run_test" },
	}
}

// --- tests ------------------------------------------------------------------

// prePushCfg is the baseline domain Config: pre-push runs test then lint.
func prePushCfg() domain.Config {
	return domain.Config{
		Hooks: domain.HookConfig{PrePush: true},
		Steps: map[string][]domain.StepName{"pre_push": {"test", "lint"}},
	}
}

// approvalCfg adds a rule that forces the approval gate on branch main.
func approvalCfg() domain.Config {
	cfg := prePushCfg()
	cfg.Rules = []domain.Rule{{
		Match: domain.Match{Branch: "main"},
		Then:  domain.Then{RequireApproval: boolPtr(true)},
	}}
	return cfg
}

func boolPtr(b bool) *bool { return &b }

// fakeSigner signs with a real ed25519 key so the written record actually
// verifies, exercising the runner's key-binding order.
type fakeSigner struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newFakeSigner(t *testing.T) *fakeSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSigner{pub: pub, priv: priv}
}

func (s *fakeSigner) PublicKey() string { return base64.StdEncoding.EncodeToString(s.pub) }
func (s *fakeSigner) Sign(payload []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, payload)), nil
}

// TestRunner_ReadOnlyBatchSharesCanonical is the cost heuristic: a parallel batch
// of purely read-only steps (test, lint) needs no per-step isolation — the policy
// contract guarantees Concurrent ⇒ non-mutating — so the runner clones nothing and
// every step runs in the canonical worktree, skipping one dep materialization per
// step (the dominant cost on large JS repos).
func TestRunner_ReadOnlyBatchSharesCanonical(t *testing.T) {
	wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: wt}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	// prePushCfg runs [test, lint] — both read-only, so they form one parallel batch.
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if len(wt.clones) != 0 {
		t.Fatalf("read-only batch must not clone; got %d clones", len(wt.clones))
	}
}

// TestRunner_ParallelBatchIsolatesOnlyWriters guards the v0.10.1 write-race fix
// while claiming the cost win: in a batch mixing a tree-writing agent (review)
// with a read-only step (test), only the writer is isolated in its own ephemeral
// worktree (torn down after the batch); the reader shares the canonical one.
func TestRunner_ParallelBatchIsolatesOnlyWriters(t *testing.T) {
	wt := &fakeWorktree{dir: "/wt", headSHA: "sha1"}
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: wt}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	cfg := prePushCfg()
	// review is a built-in agent step (writes the tree); test is read-only. Both
	// are Concurrent, so they schedule into one parallel batch.
	cfg.Steps["pre_push"] = []domain.StepName{"review", "test"}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if len(wt.clones) != 1 {
		t.Fatalf("expected only the writer (review) to be isolated, got %d clones", len(wt.clones))
	}
	if !wt.clones[0].removed {
		t.Errorf("writer clone was not torn down after the batch")
	}
}

func TestRunner_PrePushSignsProvenance(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())
	r.Signer = newFakeSigner(t)

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if !git.note.Signed() {
		t.Fatal("expected the written provenance note to be signed")
	}
	if !git.note.VerifySignature() {
		t.Error("the runner's signature must verify (public key bound into payload)")
	}
}

func TestRunner_PrePushHappyPathPushesAndRecords(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed {
		t.Fatalf("outcome = %s, want passed", res.Outcome)
	}
	if !git.pushed {
		t.Error("expected push to origin")
	}
	if !git.wroteNote || !git.notesPushed {
		t.Error("expected provenance note written and pushed")
	}
	if res.Record == nil || res.Record.EvidenceChainRoot != "root" {
		t.Error("expected a run record with the evidence chain root")
	}
	if !git.wt.removed {
		t.Error("worktree must be torn down")
	}
}

func TestRunner_FailingStepBlocksPush(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{domain.StepLint: domain.StepFail}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if git.pushed {
		t.Error("a failing step must block the push")
	}
	if kernel.approved {
		t.Error("push gate must not be reached when a step fails")
	}
}

func TestRunner_BranchMovedAborts(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", ffErr: ErrBranchMoved, wt: &fakeWorktree{dir: "/wt", headSHA: "sha2"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, approvalCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeAborted {
		t.Fatalf("outcome = %s, want aborted", res.Outcome)
	}
	if git.pushed {
		t.Error("push must not happen when the branch moved")
	}
}

func TestRunner_ErrorPaths(t *testing.T) {
	base := func() (*fakeGit, *fakeKernel) {
		return &fakeGit{root: t.TempDir(), branch: "main", head: "sha1",
				wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}},
			&fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	}

	t.Run("config load error", func(t *testing.T) {
		g, k := base()
		r := newRunner(t, g, k, fakeApprover{}, prePushCfg())
		r.Configs = fakeConfigs{err: context.Canceled}
		if _, err := r.Run(context.Background(), domain.PrePush); err == nil {
			t.Error("expected config load error to propagate")
		}
	})

	t.Run("current branch error", func(t *testing.T) {
		g, k := base()
		g.branchErr = context.Canceled
		r := newRunner(t, g, k, fakeApprover{}, prePushCfg())
		if _, err := r.Run(context.Background(), domain.PrePush); err == nil {
			t.Error("expected branch error to propagate")
		}
	})

	t.Run("unsupported hook", func(t *testing.T) {
		g, k := base()
		r := newRunner(t, g, k, fakeApprover{}, prePushCfg())
		if _, err := r.Run(context.Background(), domain.Hook("commit-msg")); err == nil {
			t.Error("expected unsupported hook error")
		}
	})

	t.Run("kernel execute error", func(t *testing.T) {
		g, k := base()
		k.execErr = context.Canceled
		r := newRunner(t, g, k, fakeApprover{}, prePushCfg())
		if _, err := r.Run(context.Background(), domain.PrePush); err == nil {
			t.Error("expected kernel execute error to propagate")
		}
	})

	t.Run("merge-base error falls back to empty base", func(t *testing.T) {
		g, k := base()
		g.mergeBaseErr = context.Canceled // no upstream; must not fail the run
		r := newRunner(t, g, k, fakeApprover{approve: true}, prePushCfg())
		res, err := r.Run(context.Background(), domain.PrePush)
		if err != nil {
			t.Fatalf("merge-base error should be tolerated, got %v", err)
		}
		if res.Outcome != domain.OutcomePassed {
			t.Errorf("outcome = %s, want passed", res.Outcome)
		}
	})
}

func TestRunner_DefaultNowAndID(t *testing.T) {
	// With Now/NewID unset the runner falls back to wall clock + timestamp id.
	g := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	k := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := &Runner{
		Git: g, Configs: fakeConfigs{cfg: prePushCfg()}, Kernels: &fakeFactory{kernel: k},
		Approver: fakeApprover{approve: true}, Settings: Settings{Version: "t", Remote: "origin"},
	}
	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record == nil || res.Record.RunID == "" || res.Record.Timestamp == "" {
		t.Error("default NewID/Now must produce a non-empty run id and timestamp")
	}
}

func TestRunner_OpensPRWhenEnabled(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	forge := &fakeForge{available: true, pr: domain.PRInfo{URL: "https://forge/pr/1", Created: true}}

	cfg := prePushCfg()
	cfg.PR = domain.PRConfig{Enabled: true, Base: "main"}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)
	r.Forge = forge

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed {
		t.Fatalf("outcome = %s, want passed", res.Outcome)
	}
	if !forge.called {
		t.Error("expected EnsurePR to be called on a passing push")
	}
	if res.PR == nil || res.PR.URL != "https://forge/pr/1" {
		t.Errorf("PR not surfaced: %+v", res.PR)
	}
	if !strings.Contains(res.Message, "https://forge/pr/1") {
		t.Errorf("message should mention the PR: %q", res.Message)
	}
	// A gate-result comment is posted by default when PRs are enabled.
	if forge.commentCount != 1 {
		t.Errorf("expected one PR comment, got %d", forge.commentCount)
	}
	if !strings.Contains(forge.commentBody, commentMarker) || !strings.Contains(forge.commentBody, "Warden gate passed") {
		t.Errorf("comment body missing marker/heading:\n%s", forge.commentBody)
	}
}

func TestRunner_SkipsPRCommentWhenDisabled(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	forge := &fakeForge{available: true, pr: domain.PRInfo{URL: "https://forge/pr/2"}}

	cfg := prePushCfg()
	cfg.PR = domain.PRConfig{Enabled: true, Comment: boolPtr(false)}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)
	r.Forge = forge

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if !forge.called {
		t.Error("PR should still be ensured")
	}
	if forge.commentCount != 0 {
		t.Errorf("comment disabled but posted %d times", forge.commentCount)
	}
}

func TestRunner_NoPRWhenDisabledOrUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		available bool
	}{
		{"disabled", false, true},
		{"unavailable", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
			kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
			forge := &fakeForge{available: tc.available}
			cfg := prePushCfg()
			cfg.PR = domain.PRConfig{Enabled: tc.enabled}
			r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)
			r.Forge = forge

			res, err := r.Run(context.Background(), domain.PrePush)
			if err != nil {
				t.Fatal(err)
			}
			if forge.called {
				t.Error("EnsurePR must not be called")
			}
			if res.PR != nil {
				t.Errorf("no PR expected, got %+v", res.PR)
			}
		})
	}
}

func preCommitCfg() domain.Config {
	return domain.Config{
		Hooks: domain.HookConfig{PreCommit: true},
		Steps: map[string][]domain.StepName{"pre_commit": {"lint"}},
	}
}

// preCommitAutoFixCfg grants the lint step an auto-fix budget, authorizing it to
// mutate the worktree and have those edits written back to the live tree.
func preCommitAutoFixCfg() domain.Config {
	cfg := preCommitCfg()
	cfg.Rules = []domain.Rule{{
		Match: domain.Match{},
		Then:  domain.Then{AutoFix: map[domain.StepName]int{"lint": 1}},
	}}
	return cfg
}

func TestRunner_PreCommitPassCapturesFixPatch(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1",
		wt: &fakeWorktree{dir: "/wt", headSHA: "sha1", diffSince: "PATCH"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	// A run with an auto-fix-budgeted step is authorized to write back, so its
	// worktree diff is captured and re-applied.
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, preCommitAutoFixCfg())

	res, err := r.Run(context.Background(), domain.PreCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed {
		t.Fatalf("outcome = %s, want passed", res.Outcome)
	}
	if res.FixPatch != "PATCH" {
		t.Errorf("fix patch = %q, want PATCH", res.FixPatch)
	}
	if git.pushed {
		t.Error("pre-commit must never push")
	}
	if !git.wt.removed {
		t.Error("worktree must be torn down")
	}
}

// TestRunner_PreCommitNoAutoFixWritesNothing is the enforcement guarantee: a
// passing pre-commit whose policy granted NO auto-fix budget must return an
// empty fix patch even when a step "wrote" files in the worktree (diffSince is
// non-empty). A read-only run must never write back to the developer's tree.
func TestRunner_PreCommitNoAutoFixWritesNothing(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1",
		wt: &fakeWorktree{dir: "/wt", headSHA: "sha1", diffSince: "STRAY-WRITE"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, preCommitCfg())

	res, err := r.Run(context.Background(), domain.PreCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed {
		t.Fatalf("outcome = %s, want passed", res.Outcome)
	}
	if res.FixPatch != "" {
		t.Errorf("fix patch = %q, want empty: a read-only run must never write back", res.FixPatch)
	}
	if git.pushed {
		t.Error("pre-commit must never push")
	}
}

func TestRunner_PreCommitFailReportsNoPatch(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1",
		wt: &fakeWorktree{dir: "/wt", headSHA: "sha1", diffSince: "PATCH"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{domain.StepLint: domain.StepFail}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, preCommitCfg())

	res, err := r.Run(context.Background(), domain.PreCommit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.FixPatch != "" {
		t.Error("a failed pre-commit must not offer a fix patch")
	}
}

func TestRunner_RejectedGateDoesNotPush(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: false}, approvalCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomeRejected {
		t.Fatalf("outcome = %s, want rejected", res.Outcome)
	}
	if git.pushed {
		t.Error("a declined gate must not push")
	}
}

// TestRunner_CancelledContextAbortsWithoutPushing proves a run whose context is
// already cancelled (Ctrl-C / SIGTERM at the CLI boundary) aborts at the push
// gate instead of falling through to auto-approval and pushing. prePushCfg does
// NOT require approval, so without the guard the gate would silently auto-approve
// and push even though the developer interrupted the run.
func TestRunner_CancelledContextAbortsWithoutPushing(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := r.Run(ctx, domain.PrePush)
	if err != nil {
		t.Fatalf("a cancelled run should abort cleanly, not error: %v", err)
	}
	if res.Outcome != domain.OutcomeAborted {
		t.Fatalf("outcome = %s, want aborted", res.Outcome)
	}
	if git.pushed {
		t.Error("a cancelled run must not push")
	}
	if kernel.approved {
		t.Error("a cancelled run must not reach the approve/push step")
	}
	if !git.wt.removed {
		t.Error("worktree must be torn down even on a cancelled run")
	}
}

// TestRunner_BatchStepErrorStillRemovesWorktree proves that when a parallel
// batch step fails operationally — the shape a recovered panic takes, since
// ExecuteBatch converts a worker panic into a per-step error — the run surfaces
// the error, does not push, and still tears down its worktree via the deferred
// cleanup (the leak the panic guard prevents).
func TestRunner_BatchStepErrorStillRemovesWorktree(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{
		outcomes: map[domain.StepName]domain.StepStatus{},
		execErr:  errors.New("step lint panicked: boom"),
	}
	cfg := prePushCfg() // steps: test, lint
	par := true
	cfg.Parallel = &par // run them as one parallel batch through ExecuteBatch
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)

	if _, err := r.Run(context.Background(), domain.PrePush); err == nil {
		t.Fatal("a batch step error must propagate as a run error")
	}
	if git.pushed {
		t.Error("a batch step error must block the push")
	}
	if !git.wt.removed {
		t.Error("worktree must be torn down even when a batch step errors")
	}
}

// fakeSBOM returns a scripted manifest.
type fakeSBOM struct{ manifests []domain.DependencyManifest }

func (f fakeSBOM) Collect(string) []domain.DependencyManifest { return f.manifests }

func TestRunner_PrePushAttachesSignedSBOM(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())
	r.Signer = newFakeSigner(t)
	r.SBOM = fakeSBOM{manifests: []domain.DependencyManifest{{Ecosystem: "go", Path: "go.sum", Digest: "sha256:abc"}}}

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if len(git.note.Dependencies) != 1 || git.note.Dependencies[0].Path != "go.sum" {
		t.Fatalf("expected the SBOM in the note, got %+v", git.note.Dependencies)
	}
	// The SBOM is part of the record, so it is covered by the signature.
	if !git.note.VerifySignature() {
		t.Error("signature must cover the attached SBOM")
	}
}

// TestRunner_ParallelBatchFirstStepFails guards the bug where, in a parallel
// batch, a non-last failing step terminated the run and the record loop then
// tried to fold the remaining outcome into the already-terminal run, surfacing
// the opaque "record step X: run is already terminal" instead of a clean
// Failed outcome naming the real culprit.
func TestRunner_ParallelBatchFirstStepFails(t *testing.T) {
	git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{domain.StepTest: domain.StepFail}}
	cfg := prePushCfg() // steps: test, lint — parallel by default
	par := true
	cfg.Parallel = &par
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, cfg)

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatalf("first-step failure in a parallel batch must not error out: %v", err)
	}
	if res.Outcome != domain.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if git.pushed {
		t.Error("a failing step must block the push")
	}
}
