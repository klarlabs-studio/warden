package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/policy"
)

// ErrBranchMoved is returned (wrapped) when the local branch advanced between
// worktree seeding and the fast-forward-back, aborting the push (§4.3).
var ErrBranchMoved = errors.New("branch moved during run")

// RunResult is the application's output DTO for a completed run, projected from
// the domain Run aggregate plus delivery-specific extras (the pre-commit fix
// patch). The domain owns the outcome; this is just its read model.
type RunResult struct {
	Outcome  domain.Outcome
	Hook     domain.Hook
	Policy   domain.ResolvedPolicy
	Findings []domain.Finding
	// Record is the provenance note written on a passing pre-push run.
	Record *domain.RunRecord
	// PR is the pull request opened or found after a passing push, if enabled.
	PR *domain.PRInfo
	// FixPatch is the worktree diff to re-apply on a passing pre-commit run.
	FixPatch string
	Message  string
}

// Runner is the application service that drives a hook invocation end to end:
// resolve policy, isolate in a worktree, run steps through the kernel, and —
// for pre-push — fast-forward back and push on approval. It owns orchestration
// and I/O; the run's lifecycle invariants live in the domain.Run aggregate. It
// depends only on ports (§4.4).
type Runner struct {
	Git      Git
	Configs  ConfigRepository
	Kernels  KernelFactory
	Approver Approver
	// Forge is optional: when set and enabled in config, a passing push opens a
	// pull request. A nil Forge disables PR creation entirely.
	Forge Forge
	// Observer is optional: when set it receives step lifecycle events for a
	// live UI. Nil means no progress reporting.
	Observer Observer
	// Signer is optional: when set, a passing pre-push run's provenance note is
	// signed. A nil Signer (or a signing failure) leaves the note unsigned.
	Signer   Signer
	Settings Settings
	// Now and NewID are injected for deterministic tests.
	Now   func() time.Time
	NewID func() string
}

// Settings carries run-invariant configuration.
type Settings struct {
	Version string
	Remote  string
}

// Run executes the pipeline for hook against the repository.
func (r *Runner) Run(ctx context.Context, hook domain.Hook) (RunResult, error) {
	cfg, err := r.Configs.Load()
	if err != nil {
		return RunResult{}, err
	}
	branch, err := r.Git.CurrentBranch()
	if err != nil {
		return RunResult{}, fmt.Errorf("current branch: %w", err)
	}

	diff, err := r.diffForRisk(hook, branch)
	if err != nil {
		return RunResult{}, err
	}
	risk := cfg.Risk.Thresholds().Classify(diff)

	resolved := policy.Resolve(cfg, policy.Input{Hook: hook, Branch: branch, Paths: diff.Paths, Risk: risk})
	resolved.Commands = cfg.Commands
	resolved.AgentCommands = cfg.AgentCommands

	switch hook {
	case domain.PreCommit:
		return r.runPreCommit(ctx, resolved, branch, diff)
	case domain.PrePush:
		return r.runPrePush(ctx, resolved, branch, diff, cfg.PR)
	default:
		return RunResult{}, fmt.Errorf("unsupported hook %q", hook)
	}
}

// diffForRisk computes the diff stats that drive risk and path matching: the
// staged index for pre-commit, else HEAD against its merge-base with origin.
func (r *Runner) diffForRisk(hook domain.Hook, branch string) (domain.DiffStats, error) {
	if hook == domain.PreCommit {
		return r.Git.StagedDiffStats()
	}
	base, err := r.Git.MergeBase(r.Settings.Remote + "/" + branch)
	if err != nil {
		// No upstream yet: fall back to diffing against the empty base so a
		// first push still gets sensible stats.
		base = ""
	}
	return r.Git.DiffStats(base)
}

// runPreCommit runs the fast local subset in a worktree seeded from HEAD plus
// staged changes, then reports any fixes to re-apply to the real index (§4.2).
func (r *Runner) runPreCommit(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats) (RunResult, error) {
	wt, err := r.Git.SeedWorktreeFromHead()
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer wt.Remove()

	sc := StepContext{Hook: domain.PreCommit, WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands}
	run := r.newRun(domain.PreCommit, resolved, branch)

	if _, err := r.runValidation(ctx, run, resolved, sc, nil); err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() {
		return r.result(run, ""), nil
	}
	if err := run.Pass(); err != nil {
		return RunResult{}, err
	}
	// Capture any auto-fixes so the hook can re-apply them to the live tree.
	patch, err := wt.DiffSince()
	if err != nil {
		return RunResult{}, fmt.Errorf("compute fix patch: %w", err)
	}
	return r.result(run, patch), nil
}

// runPrePush runs the full pipeline in a worktree cloned from the branch tip,
// then fast-forwards back and pushes on approval (§4.3).
func (r *Runner) runPrePush(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats, prCfg domain.PRConfig) (RunResult, error) {
	seedTip, err := r.Git.HeadSHA()
	if err != nil {
		return RunResult{}, err
	}
	wt, err := r.Git.SeedWorktreeFromBranch(branch)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer wt.Remove()

	sc := StepContext{Hook: domain.PrePush, WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands}
	run := r.newRun(domain.PrePush, resolved, branch)

	// The push closure runs only after the kernel's approval gate clears. It
	// performs the real fast-forward-back, push, and note write (§4.3 step 2).
	push := func(ctx context.Context) (domain.StepResult, error) {
		finalSHA, err := wt.HeadSHA()
		if err != nil {
			return domain.StepResult{}, err
		}
		if err := r.Git.FastForwardTo(branch, finalSHA, seedTip); err != nil {
			return domain.StepResult{}, fmt.Errorf("%w: %v", ErrBranchMoved, err)
		}
		if err := r.Git.Push(r.Settings.Remote, branch); err != nil {
			return domain.StepResult{}, fmt.Errorf("push: %w", err)
		}
		return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
	}

	kernel, err := r.runValidation(ctx, run, resolved, sc, push)
	if err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() { // a validation step failed
		return r.result(run, ""), nil
	}

	if err := r.resolvePushGate(ctx, run, kernel); err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() { // rejected or aborted at the gate
		return r.result(run, ""), nil
	}

	// Provenance: verify the run-level evidence chain and write the note.
	record, err := r.buildRecord(kernel, run)
	if err != nil {
		return RunResult{}, err
	}
	r.sign(record)
	if finalSHA, err := r.Git.HeadSHA(); err == nil {
		// Note-push is best-effort: a failed note never fails the gate (§9).
		if err := r.Git.WriteNote(finalSHA, *record); err == nil {
			_ = r.Git.PushNotes(r.Settings.Remote)
		}
	}
	if err := run.MarkPushed(*record, "warden pushed the gated commit(s); local branch fast-forwarded"); err != nil {
		return RunResult{}, err
	}

	res := r.result(run, "")
	// PR creation is best-effort and post-push: a forge failure never unwinds a
	// push that already succeeded (§4.3). Only run it when enabled and usable.
	if prCfg.Enabled && r.Forge != nil && r.Forge.Available() {
		if pr, err := r.Forge.EnsurePR(ctx, branch, prCfg.Base); err == nil {
			res.PR = &pr
			if pr.URL != "" {
				res.Message += "; PR " + pr.URL
			}
		}
	}
	return res, nil
}

// runValidation builds the run's kernel and folds each resolved step's outcome
// into the aggregate, stopping as soon as the aggregate reaches a terminal
// state. Independent (read-only) steps run concurrently in batches; steps that
// write the worktree stay sequential barriers (see scheduleBatches). It returns
// the kernel so the caller can resolve the push gate.
func (r *Runner) runValidation(ctx context.Context, run *domain.Run, resolved domain.ResolvedPolicy, sc StepContext, push PushFunc) (Kernel, error) {
	var priors []domain.Finding
	kernel, err := r.Kernels.New(resolved, sc, &priors, push)
	if err != nil {
		return nil, err
	}
	for _, batch := range resolved.Batches() {
		if err := r.runBatch(ctx, run, kernel, batch); err != nil {
			return nil, err
		}
		if run.IsTerminal() {
			break
		}
	}
	return kernel, nil
}

// runBatch runs one scheduled batch and folds its outcomes into the aggregate in
// declared order. A singleton batch takes the plain Execute path; a multi-step
// batch runs concurrently through ExecuteBatch, emitting a start event for every
// step up front so the UI shows them running together, and a finish event per
// step as it completes.
func (r *Runner) runBatch(ctx context.Context, run *domain.Run, kernel Kernel, batch []domain.StepName) error {
	if len(batch) == 1 {
		step := batch[0]
		r.notify(StepEvent{Step: step, Phase: StepStarted})
		out, err := kernel.Execute(ctx, step)
		if err != nil {
			return fmt.Errorf("step %s: %w", step, err)
		}
		if err := run.RecordStep(out.Result); err != nil {
			return err
		}
		r.notify(StepEvent{Step: step, Phase: StepFinished, Result: out.Result})
		return nil
	}

	for _, step := range batch {
		r.notify(StepEvent{Step: step, Phase: StepStarted})
	}
	onFinish := func(step domain.StepName, out StepOutcome) {
		r.notify(StepEvent{Step: step, Phase: StepFinished, Result: out.Result})
	}
	outcomes, err := kernel.ExecuteBatch(ctx, batch, onFinish)
	if err != nil {
		return err
	}
	for _, out := range outcomes {
		if err := run.RecordStep(out.Result); err != nil {
			return err
		}
	}
	return nil
}

// notify forwards a step event to the Observer when one is set.
func (r *Runner) notify(e StepEvent) {
	if r.Observer != nil {
		r.Observer.OnStep(e)
	}
}

// resolvePushGate drives the write-external push action through its approval
// pause: the aggregate decides whether a human is needed, the approver answers,
// and a push failure aborts the run. On success the push executor has already
// performed the real push.
func (r *Runner) resolvePushGate(ctx context.Context, run *domain.Run, kernel Kernel) error {
	gate, err := kernel.Execute(ctx, domain.StepPush)
	if err != nil {
		return err
	}
	if !gate.NeedsApproval {
		return nil
	}

	decision := autoApproval()
	if run.RequiresApproval() {
		decision, err = r.Approver.Approve(ctx, ApprovalRequest{
			Hook: run.Hook(), Branch: run.Branch(), Steps: run.Policy().Steps, Findings: run.Findings(), Risk: run.Policy().Risk,
		})
		if err != nil {
			return err
		}
	}
	if !decision.Approved {
		_, _ = kernel.Reject(ctx, gate.SessionID, decision.Principal, decision.Rationale)
		return run.Reject("approval declined")
	}

	if _, err := kernel.Approve(ctx, gate.SessionID, decision.Principal, decision.Rationale); err != nil {
		// The push executor's failure crosses the axi boundary as a message, so
		// branch-moved is matched by substring as well as errors.Is. Either way
		// a failed push aborts — never a successful gate.
		if errors.Is(err, ErrBranchMoved) || strings.Contains(err.Error(), ErrBranchMoved.Error()) {
			return run.Abort("branch changed mid-run; re-push")
		}
		return run.Abort("push failed: " + err.Error())
	}
	return nil
}

// sign attaches an ed25519 signature to the record when a Signer is configured.
// Signing is best-effort: a failure leaves the note unsigned rather than failing
// a push that has already succeeded (§9).
func (r *Runner) sign(record *domain.RunRecord) {
	if r.Signer == nil {
		return
	}
	// Set the public key first so SigningPayload binds it into the signature.
	record.PublicKey = r.Signer.PublicKey()
	payload, err := record.SigningPayload()
	if err != nil {
		record.PublicKey = ""
		return
	}
	sig, err := r.Signer.Sign(payload)
	if err != nil {
		record.PublicKey = ""
		return
	}
	record.Signature = sig
}

// buildRecord finalizes the evidence chain into a provenance RunRecord (§9).
func (r *Runner) buildRecord(kernel Kernel, run *domain.Run) (*domain.RunRecord, error) {
	root, entries, err := kernel.Finalize()
	if err != nil {
		return nil, fmt.Errorf("verify evidence chain: %w", err)
	}
	return &domain.RunRecord{
		RunID:             string(run.ID()),
		Timestamp:         r.now().UTC().Format(time.RFC3339),
		WardenVersion:     r.Settings.Version,
		Agent:             run.Policy().Agents,
		StepsRun:          run.Policy().Steps,
		MatchedRules:      run.Policy().MatchedRules,
		EvidenceChainRoot: root,
		Evidence:          entries,
	}, nil
}

// newRun mints a run aggregate with a fresh identity.
func (r *Runner) newRun(hook domain.Hook, resolved domain.ResolvedPolicy, branch string) *domain.Run {
	id, err := domain.NewRunID(r.newID())
	if err != nil {
		// newID never returns empty; fall back defensively.
		id = domain.RunID("run_unknown")
	}
	return domain.NewRun(id, hook, resolved, branch)
}

// result projects the aggregate into the application's output DTO.
func (r *Runner) result(run *domain.Run, patch string) RunResult {
	return RunResult{
		Outcome:  run.Outcome(),
		Hook:     run.Hook(),
		Policy:   run.Policy(),
		Findings: run.Findings(),
		Record:   run.Record(),
		FixPatch: patch,
		Message:  run.Message(),
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) newID() string {
	if r.NewID != nil {
		return r.NewID()
	}
	return "run_" + time.Now().UTC().Format("20060102T150405.000000000")
}

// autoApproval is the decision for a clean run no rule flagged for review.
func autoApproval() Decision {
	return Decision{Approved: true, Principal: "warden-auto", Rationale: "clean run, no rule required approval"}
}
