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

// Outcome classifies how a run ended.
type Outcome string

const (
	OutcomePassed   Outcome = "passed"
	OutcomeFailed   Outcome = "failed"   // a step rejected the change
	OutcomeRejected Outcome = "rejected" // approver declined at the gate
	OutcomeAborted  Outcome = "aborted"  // operational failure (e.g. branch moved)
)

// RunResult is the terminal report of a run for the delivery layer.
type RunResult struct {
	Outcome  Outcome
	Hook     domain.Hook
	Policy   domain.ResolvedPolicy
	Findings []domain.Finding
	// Record is the provenance note written on a passing pre-push run.
	Record *domain.RunRecord
	// FixPatch is the worktree diff to re-apply on a passing pre-commit run.
	FixPatch string
	Message  string
}

// Runner drives a hook invocation end to end: resolve policy, isolate in a
// worktree, run steps through the kernel, and — for pre-push — fast-forward
// back and push on approval. It depends only on ports (§4.4).
type Runner struct {
	Git      Git
	Kernels  KernelFactory
	Approver Approver
	Config   Config
	// Now and NewID are injected for deterministic tests.
	Now   func() time.Time
	NewID func() string
}

// Config carries run-invariant settings.
type Config struct {
	Version string
	Remote  string
}

// Run executes the pipeline for hook against the repository.
func (r *Runner) Run(ctx context.Context, hook domain.Hook) (RunResult, error) {
	cfg, err := policy.Load(r.repoRoot())
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
	risk := domain.RiskConfig(cfg.Risk).Thresholds().Classify(diff)

	resolved := policy.Resolve(cfg, policy.Input{
		Hook:   hook,
		Branch: branch,
		Paths:  diff.Paths,
		Risk:   risk,
	})
	resolved.Commands = cfg.Commands

	switch hook {
	case domain.PreCommit:
		return r.runPreCommit(ctx, resolved, branch, diff)
	case domain.PrePush:
		return r.runPrePush(ctx, resolved, branch, diff)
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
	base, err := r.Git.MergeBase(r.Config.Remote + "/" + branch)
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

	sc := StepContext{
		Hook:        domain.PreCommit,
		WorktreeDir: wt.Dir(),
		Branch:      branch,
		Diff:        diff,
		Commands:    resolved.Commands,
	}
	res, err := r.runSteps(ctx, resolved, sc, nil)
	if err != nil {
		return RunResult{}, err
	}
	if res.Outcome != OutcomePassed {
		return res, nil
	}
	// Capture any auto-fixes so the hook can re-apply them to the live tree.
	patch, err := wt.DiffSince()
	if err != nil {
		return RunResult{}, fmt.Errorf("compute fix patch: %w", err)
	}
	res.FixPatch = patch
	return res, nil
}

// runPrePush runs the full pipeline in a worktree cloned from the branch tip,
// then fast-forwards back and pushes on approval (§4.3).
func (r *Runner) runPrePush(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats) (RunResult, error) {
	seedTip, err := r.Git.HeadSHA()
	if err != nil {
		return RunResult{}, err
	}
	wt, err := r.Git.SeedWorktreeFromBranch(branch)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer wt.Remove()

	sc := StepContext{
		Hook:        domain.PrePush,
		WorktreeDir: wt.Dir(),
		Branch:      branch,
		Diff:        diff,
		Commands:    resolved.Commands,
	}

	var findings []domain.Finding
	runID := r.newID()

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
		if err := r.Git.Push(r.Config.Remote, branch); err != nil {
			return domain.StepResult{}, fmt.Errorf("push: %w", err)
		}
		return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass, Summary: "pushed " + finalSHA[:min(12, len(finalSHA))]}, nil
	}

	kernel, err := r.Kernels.New(resolved, sc, &findings, push)
	if err != nil {
		return RunResult{}, err
	}

	// 1) Validation steps. Any failure aborts before the push gate.
	needsApproval := false
	for _, step := range resolved.Steps {
		out, err := kernel.Execute(ctx, step)
		if err != nil {
			return RunResult{}, fmt.Errorf("step %s: %w", step, err)
		}
		findings = appendUnique(findings, out.Result.Findings)
		switch out.Result.Status {
		case domain.StepFail:
			return RunResult{
				Outcome:  OutcomeFailed,
				Hook:     domain.PrePush,
				Policy:   resolved,
				Findings: findings,
				Message:  fmt.Sprintf("step %s failed", step),
			}, nil
		case domain.StepNeedsApproval:
			needsApproval = true
		}
	}

	// 2) Push gate: the kernel pauses the write-external push at approval.
	gate, err := kernel.Execute(ctx, domain.StepPush)
	if err != nil {
		return RunResult{}, err
	}
	if gate.NeedsApproval {
		decision, err := r.decide(ctx, resolved, branch, findings, needsApproval)
		if err != nil {
			return RunResult{}, err
		}
		if !decision.Approved {
			_, _ = kernel.Reject(ctx, gate.SessionID, decision.Principal, decision.Rationale)
			return RunResult{Outcome: OutcomeRejected, Hook: domain.PrePush, Policy: resolved, Findings: findings, Message: "approval declined"}, nil
		}
		if _, err := kernel.Approve(ctx, gate.SessionID, decision.Principal, decision.Rationale); err != nil {
			// The push executor's failure crosses the axi boundary as a
			// message, so branch-moved is matched by substring rather than
			// errors.Is. Either way a failed push aborts the run — it must
			// never be reported as a successful gate.
			if errors.Is(err, ErrBranchMoved) || strings.Contains(err.Error(), ErrBranchMoved.Error()) {
				return RunResult{Outcome: OutcomeAborted, Hook: domain.PrePush, Policy: resolved, Findings: findings,
					Message: "branch changed mid-run; re-push"}, nil
			}
			return RunResult{Outcome: OutcomeAborted, Hook: domain.PrePush, Policy: resolved, Findings: findings,
				Message: "push failed: " + err.Error()}, nil
		}
	}

	// 3) Provenance: verify the run-level evidence chain and write the note.
	record, err := r.buildRecord(kernel, resolved, runID)
	if err != nil {
		return RunResult{}, err
	}
	finalSHA, err := r.Git.HeadSHA()
	if err == nil {
		// Note-push is best-effort: a failed note never fails the gate (§9).
		if err := r.Git.WriteNote(finalSHA, *record); err == nil {
			_ = r.Git.PushNotes(r.Config.Remote)
		}
	}

	return RunResult{
		Outcome:  OutcomePassed,
		Hook:     domain.PrePush,
		Policy:   resolved,
		Findings: findings,
		Record:   record,
		Message:  "warden pushed the gated commit(s); local branch fast-forwarded",
	}, nil
}

// runSteps executes a step sequence with no push gate (pre-commit path).
func (r *Runner) runSteps(ctx context.Context, resolved domain.ResolvedPolicy, sc StepContext, push PushFunc) (RunResult, error) {
	var findings []domain.Finding
	kernel, err := r.Kernels.New(resolved, sc, &findings, push)
	if err != nil {
		return RunResult{}, err
	}
	for _, step := range resolved.Steps {
		out, err := kernel.Execute(ctx, step)
		if err != nil {
			return RunResult{}, fmt.Errorf("step %s: %w", step, err)
		}
		findings = appendUnique(findings, out.Result.Findings)
		if out.Result.Status == domain.StepFail {
			return RunResult{Outcome: OutcomeFailed, Hook: sc.Hook, Policy: resolved, Findings: findings,
				Message: fmt.Sprintf("step %s failed", step)}, nil
		}
	}
	return RunResult{Outcome: OutcomePassed, Hook: sc.Hook, Policy: resolved, Findings: findings}, nil
}

// decide resolves the approval gate: clean runs that no rule flagged auto-pass;
// anything requiring approval or carrying unresolved findings goes to the
// Approver.
func (r *Runner) decide(ctx context.Context, resolved domain.ResolvedPolicy, branch string, findings []domain.Finding, needsApproval bool) (Decision, error) {
	if !resolved.RequireApproval && !needsApproval && !hasBlocking(findings) {
		return Decision{Approved: true, Principal: "warden-auto", Rationale: "clean run, no rule required approval"}, nil
	}
	return r.Approver.Approve(ctx, ApprovalRequest{
		Hook:     resolved.Hook,
		Branch:   branch,
		Steps:    resolved.Steps,
		Findings: findings,
		Risk:     resolved.Risk,
	})
}

// buildRecord finalizes the evidence chain into a provenance RunRecord (§9).
func (r *Runner) buildRecord(kernel Kernel, resolved domain.ResolvedPolicy, runID string) (*domain.RunRecord, error) {
	root, entries, err := kernel.Finalize()
	if err != nil {
		return nil, fmt.Errorf("verify evidence chain: %w", err)
	}
	return &domain.RunRecord{
		RunID:             runID,
		Timestamp:         r.now().UTC().Format(time.RFC3339),
		WardenVersion:     r.Config.Version,
		Agent:             resolved.Agents,
		StepsRun:          resolved.Steps,
		MatchedRules:      resolved.MatchedRules,
		EvidenceChainRoot: root,
		Evidence:          entries,
	}, nil
}

func (r *Runner) repoRoot() string {
	// The Git port operates on the repo root already; policy loads from there.
	if g, ok := r.Git.(interface{ Root() string }); ok {
		return g.Root()
	}
	return "."
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

// hasBlocking reports whether any finding is high severity.
func hasBlocking(fs []domain.Finding) bool {
	for _, f := range fs {
		if f.Severity == domain.SeverityHigh {
			return true
		}
	}
	return false
}

// appendUnique appends findings not already present (by file+line+message).
func appendUnique(dst, add []domain.Finding) []domain.Finding {
	seen := map[string]bool{}
	for _, f := range dst {
		seen[findingKey(f)] = true
	}
	for _, f := range add {
		if !seen[findingKey(f)] {
			dst = append(dst, f)
			seen[findingKey(f)] = true
		}
	}
	return dst
}

func findingKey(f domain.Finding) string {
	return fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Message)
}
