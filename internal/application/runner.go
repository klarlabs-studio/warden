package application

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// GitCompletesPush is true when a passing pre-push left the actual push to
	// git rather than performing it itself. The hook can then exit 0 and let git
	// report the push honestly, instead of having to fail on purpose and warn
	// the developer to ignore git's resulting `error:` line (#89).
	GitCompletesPush bool
	Message          string
	// Warnings are non-fatal notices the run wants the developer to see —
	// currently only a WARDEN_ALLOW_DISCARD override naming the commits it
	// force-pushed over. Kept out of Message so the verdict line stays the
	// verdict, and out of the application layer's own stdout so delivery
	// keeps owning I/O.
	Warnings []string
	// Blocker names the environmental obstacle that ended a failed run (a tool's
	// lock, a missing toolchain) rather than the change itself. BlockerNone means
	// the verdict is about the change. Delivery maps it to a distinct exit code.
	Blocker domain.Blocker
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
	Signer Signer
	// SBOM is optional: when set, a passing pre-push run records its dependency
	// lockfile digests in the provenance note.
	SBOM     SBOM
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
		return r.runPrePush(ctx, resolved, branch, diff, cfg)
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
	wt, err := r.Git.SeedWorktreeFromHead(resolved.MaterializeDeps)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer func() { _ = wt.Remove() }()

	sc := r.withStream(StepContext{Hook: domain.PreCommit, WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands, SecurityScan: resolved.SecurityScan, Remote: r.Settings.Remote})
	run := r.newRun(domain.PreCommit, resolved, branch)

	if _, err := r.runValidation(ctx, run, resolved, sc, nil, wt); err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() {
		return r.result(run, ""), nil
	}
	if err := run.Pass(); err != nil {
		return RunResult{}, err
	}
	// Capture any auto-fixes so the hook can re-apply them to the live tree — but
	// only when a step was actually authorized to fix. wt.DiffSince() is the whole
	// worktree diff and gets written back to the developer's live tree verbatim;
	// if no step held an auto-fix budget this run is read-only, so any writes a
	// step made (a review/intent agent, a lint with a stray --fix) are unsanctioned
	// and must never land in the dev's tree. AutoFixBudget bounds retry counts, not
	// who may mutate — AuthorizesFix is the enforcement boundary (§4.2).
	var patch string
	if resolved.AuthorizesFix() {
		patch, err = wt.DiffSince()
		if err != nil {
			return RunResult{}, fmt.Errorf("compute fix patch: %w", err)
		}
	}
	return r.result(run, patch), nil
}

// ErrPushRewritesHistory is returned when a push would discard commits the
// remote has and the repo's policy forbids rewriting (push.force: never).
var ErrPushRewritesHistory = errors.New("push would rewrite the remote branch's history")

// ErrPushDiscardsRemoteWork is returned when a forced push would delete commits
// that exist only on the remote — someone else's work on a shared branch, as
// opposed to our own commits in their pre-rewrite form.
//
// push.force: lease exists so a branch rebased onto an updated base can still be
// published (#85). It is permission to replace OUR history, and it is not a
// judgement that anything on the remote is expendable. --force-with-lease does
// not draw that line either: it only asserts the remote has not moved since our
// last fetch, so once we have fetched a colleague's commit the lease is
// satisfied and the push destroys it silently.
var ErrPushDiscardsRemoteWork = errors.New("push would discard commits that exist only on the remote")

// pushForce decides how to push a branch whose history may no longer
// fast-forward from the remote — the ordinary result of rebasing onto an
// updated base.
//
// Warden performs the push itself, so git's pre-push hook is handed no signal
// that the developer typed --force and a plain push fails as non-fast-forward
// regardless. The rewrite therefore has to be detected (the remote tip is not
// an ancestor of ours) and decided by policy rather than inherited from the
// command line.
//
// A fast-forward never forces, whatever the policy says: the force flag is
// reserved for the case that actually needs it, so an ordinary push carries no
// extra power. When a rewrite IS needed and policy forbids it, the run fails
// with a message naming the knob — the alternative the developer would
// otherwise reach for is `git push --no-verify`, which skips the gate and
// writes no provenance at all.
func (r *Runner) pushForce(cfg domain.Config, branch string) (domain.PushForce, string, error) {
	rewrites, err := r.Git.PushRewritesHistory(r.Settings.Remote, branch)
	if err != nil || !rewrites {
		// An unreadable ancestry is not a license to force: fall back to the plain
		// push and let git refuse it, as it would without warden.
		return domain.ForceNever, "", nil
	}
	mode := cfg.PushForceMode()
	if mode == domain.ForceNever {
		return "", "", fmt.Errorf("%w: %s has been rebased or amended, and this repo sets push.force: never. "+
			"Rewrite it deliberately (git push --force-with-lease) or allow it with push.force: lease in .warden.yaml",
			ErrPushRewritesHistory, branch)
	}
	// A lease is permission to replace OUR OWN pre-rewrite commits, never to
	// discard someone else's. Refuse when the remote carries work with no
	// patch-equivalent locally — see ErrPushDiscardsRemoteWork.
	if lost, err := r.Git.UnmergedRemoteCommits(r.Settings.Remote, branch); err != nil || len(lost) > 0 {
		if err != nil {
			// Cannot tell whose work is on the remote: do not force on a guess.
			return "", "", fmt.Errorf("%w: could not determine whether %s/%s carries work not in your history: %v",
				ErrPushDiscardsRemoteWork, r.Settings.Remote, branch, err)
		}
		// The scoped override. UnmergedRemoteCommits already suppresses the guard
		// for commits it can prove are our own pre-rewrite tips (committed by us
		// AND once reachable from this branch's reflog), which covers the ordinary
		// rebase. It cannot prove that when the reflog is absent — a fresh clone,
		// another machine, a rewrite older than the scan limit — or when the
		// commit was committed by someone else, as GitHub's web UI and "Update
		// branch" both do.
		//
		// Without a proportionate escape those cases leave `git push --no-verify`
		// as the only way through, which skips test, lint AND security-scan and
		// writes no provenance. A guard whose only override is the nuclear one
		// teaches people to reach for the nuclear one, so the scan stops running
		// exactly on the branches that just absorbed someone else's changes.
		//
		// This bypasses THIS check and nothing else: every step still runs and the
		// note is still written. The dropped commits are named on the way past so
		// the decision is recorded rather than silent.
		if allowDiscard() {
			return mode, fmt.Sprintf(
				"WARDEN_ALLOW_DISCARD set — force-pushed over %d commit(s) on %s/%s that were not in your history:\n  %s",
				len(lost), r.Settings.Remote, branch, strings.Join(lost, "\n  ")), nil
		}
		return "", "", fmt.Errorf("%w: %s/%s carries %d commit(s) that are not in your history:\n  %s\n"+
			"If they are someone else's, integrate first: `git pull --rebase`, then push.\n"+
			"If they are your own pre-rewrite commits that warden could not verify (a fresh clone\n"+
			"has no reflog to check against), re-push with WARDEN_ALLOW_DISCARD=1 — that skips this\n"+
			"one check and keeps test, lint and security-scan running, unlike --no-verify",
			ErrPushDiscardsRemoteWork, r.Settings.Remote, branch, len(lost), strings.Join(lost, "\n  "))
	}
	return mode, "", nil
}

// allowDiscard reports whether the developer has explicitly accepted losing the
// remote-only commits on this push. Deliberately an environment variable and not
// a flag: the developer types `git push`, and warden runs as its pre-push hook,
// so there is no warden command line to put a flag on.
func allowDiscard() bool {
	v := strings.TrimSpace(os.Getenv("WARDEN_ALLOW_DISCARD"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// delegateToGit reports whether the push git is ALREADY about to perform is
// byte-for-byte the one warden would perform, so warden can stand aside and let
// git do it.
//
// Warden normally pushes itself and then fails the hook on purpose, because git
// pushes the ref value it captured BEFORE the hook ran: if a step rewrote the
// branch (an auto-fix, an amending agent step), git would publish the
// *unvalidated* pre-fix commit. Standing aside is only safe when nothing was
// rewritten.
//
// It is also unsafe when the push needs a force. Git's own push is not forced,
// so a rebased branch would simply be rejected — and warden's whole reason for
// supporting a lease (#85) is that the alternative is a gate bypass.
//
// Finally, it is unsafe when a PR is to be opened. PR creation runs AFTER the
// push, and git's push does not happen until the hook has exited — so a
// delegating run reaches `gh pr create` while the branch is still absent from
// the remote. On a first push that fails ("head branch not found"), and because
// PR creation is best-effort it fails SILENTLY: no PR, no error. Warden pushes
// itself in that case so the branch exists by the time the forge is called.
//
// When all three hold, delegating is strictly better than pushing ourselves: git
// reports the push it actually performed, with a real exit code, instead of
// warden succeeding and then having to warn the developer to ignore the
// `error:` git prints because warden deliberately failed the hook (#89).
func (r *Runner) delegateToGit(finalSHA, seedTip string, force domain.PushForce, willOpenPR bool) bool {
	return finalSHA == seedTip && force == domain.ForceNever && !willOpenPR
}

// runPrePush runs the full pipeline in a worktree cloned from the branch tip,
// then fast-forwards back and pushes on approval (§4.3).
func (r *Runner) runPrePush(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats, cfg domain.Config) (RunResult, error) {
	prCfg := cfg.PR
	seedTip, err := r.Git.HeadSHA()
	if err != nil {
		return RunResult{}, err
	}
	wt, err := r.Git.SeedWorktreeFromBranch(branch, resolved.MaterializeDeps)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer func() { _ = wt.Remove() }()

	sc := r.withStream(StepContext{Hook: domain.PrePush, WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands, SecurityScan: resolved.SecurityScan, Remote: r.Settings.Remote, PRBase: cfg.PR.Base})
	run := r.newRun(domain.PrePush, resolved, branch)

	// spanBase is the commit this push starts from, captured BEFORE the push
	// moves the remote-tracking ref. It becomes the record's CoversFrom, so the
	// note states the span the run vouches for and not just its tip (#86).
	var spanBase string
	// gitCompletes records that warden left the push to git — see delegateToGit.
	var gitCompletes bool
	// discardWarning carries a WARDEN_ALLOW_DISCARD override up to the caller so
	// delivery can print which commits were force-pushed over.
	var discardWarning string
	// willOpenPR is resolved once, before the gate, because the delegation
	// decision is made inside the push closure but PR creation happens after it.
	willOpenPR := prCfg.Enabled && r.Forge != nil && r.Forge.Available()

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
		// Best-effort: a span we cannot determine is simply not claimed, leaving
		// the note attesting its tip alone. Provenance never fails the gate.
		spanBase, _ = r.Git.PushSpanBase(r.Settings.Remote, branch)
		force, warn, err := r.pushForce(cfg, branch)
		if err != nil {
			return domain.StepResult{}, err
		}
		if warn != "" {
			discardWarning = warn
		}
		if r.delegateToGit(finalSHA, seedTip, force, willOpenPR) {
			gitCompletes = true
			return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
		}
		if err := r.Git.Push(r.Settings.Remote, branch, force); err != nil {
			return domain.StepResult{}, fmt.Errorf("push: %w", err)
		}
		return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
	}

	kernel, err := r.runValidation(ctx, run, resolved, sc, push, wt)
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
	// Attach the SBOM before signing so the dependency digests are covered by the
	// signature. Best-effort: a collector that finds nothing leaves it empty.
	if r.SBOM != nil {
		record.Dependencies = r.SBOM.Collect(sc.WorktreeDir)
	}
	// Bind the record to the commit it attests BEFORE signing, so the commit SHA
	// is covered by the signature and the note can't be transplanted to another
	// commit. The same SHA is the note key. If HEAD can't be read the record stays
	// unbound and no note is written (best-effort provenance never fails the gate).
	finalSHA, shaErr := r.Git.HeadSHA()
	if shaErr == nil {
		record.CommitSHA = finalSHA
		// Claim the span only when it is strictly below the tip. A base equal to
		// (or absent from) the tip covers nothing extra, and claiming an empty or
		// self-referential span would be noise in a signed statement.
		if spanBase != "" && spanBase != finalSHA {
			record.CoversFrom = spanBase
		}
	}
	r.sign(record)
	if shaErr == nil {
		// Note-push is best-effort: a failed note never fails the gate (§9).
		if err := r.Git.WriteNote(finalSHA, *record); err == nil {
			_ = r.Git.PushNotes(r.Settings.Remote)
		}
	}
	msg := pushedMessage(finalSHA, r.Settings.Remote, branch, gitCompletes)
	if err := run.MarkPushed(*record, msg); err != nil {
		return RunResult{}, err
	}

	res := r.result(run, "")
	res.GitCompletesPush = gitCompletes
	if discardWarning != "" {
		res.Warnings = append(res.Warnings, discardWarning)
	}
	// PR creation is best-effort and post-push: a forge failure never unwinds a
	// push that already succeeded (§4.3). Only run it when enabled and usable.
	if willOpenPR {
		if pr, err := r.Forge.EnsurePR(ctx, branch, prCfg.Base); err == nil {
			res.PR = &pr
			if pr.URL != "" {
				res.Message += "; PR " + pr.URL
			}
			// Post the gate summary on the PR — best-effort, like PR creation: a
			// comment failure never unwinds a push that already succeeded.
			if prCfg.CommentEnabled() {
				_ = r.Forge.Comment(ctx, branch, prComment(res, branch))
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
func (r *Runner) runValidation(ctx context.Context, run *domain.Run, resolved domain.ResolvedPolicy, sc StepContext, push PushFunc, wt Worktree) (Kernel, error) {
	// Per-step worktree isolation: steps in a parallel batch each run in their own
	// ephemeral worktree cloned from the canonical one, so a step's writes can't
	// race a sibling. The registry maps a step to its worktree during a batch; an
	// unregistered step (a sequential barrier) falls back to the canonical wt.
	reg := newWorktreeRegistry()
	sc.WorktreeFor = reg.dirFor

	var priors []domain.Finding
	kernel, err := r.Kernels.New(resolved, sc, &priors, push)
	if err != nil {
		return nil, err
	}
	for _, batch := range resolved.Batches() {
		if err := r.runBatch(ctx, run, kernel, batch, wt, reg, resolved); err != nil {
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
func (r *Runner) runBatch(ctx context.Context, run *domain.Run, kernel Kernel, batch []domain.StepName, wt Worktree, reg *worktreeRegistry, resolved domain.ResolvedPolicy) error {
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

	// Isolate only the steps that mutate the tree (tree-writing agents) in their
	// own ephemeral worktree cloned from the canonical one, so a writer can't race
	// a sibling writer or leak mid-write state to a concurrent reader — the
	// v0.10.1 write-race fix. Read-only steps (lint/test/scan) can't race: the
	// policy contract makes Concurrent ⇒ non-mutating, so they share the canonical
	// worktree (an unregistered step falls back to it) and skip the per-step clone
	// plus its dep materialization entirely — the dominant cost on large JS repos,
	// one node_modules copy per step. An all-read-only batch clones nothing. A
	// clone failure is best-effort: the writer falls back to the canonical
	// worktree. All clones are torn down and the registry cleared after the batch,
	// discarding the steps' side-effects.
	if wt != nil {
		var clones []Worktree
		defer func() {
			for _, c := range clones {
				_ = c.Remove()
			}
			reg.reset()
		}()
		for _, step := range batch {
			if !resolved.WritesTree(step) {
				continue // read-only: share the canonical worktree, no clone
			}
			clone, err := wt.Clone(resolved.MaterializeDeps)
			if err != nil {
				continue // best-effort; this step uses the canonical worktree
			}
			clones = append(clones, clone)
			reg.set(step, clone.Dir())
		}
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
	// Fold outcomes in declared order. A failing step terminates the run, so
	// stop before recording into an already-terminal run — otherwise a second
	// failing/any step in the same parallel batch surfaces the opaque
	// "record step X: run is already terminal" instead of the real failure.
	for _, out := range outcomes {
		if run.IsTerminal() {
			break
		}
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

// streamLine forwards one line of a step's live output to the Observer. It is
// the sink the kernel binds into each step's OnOutput; it is a no-op without an
// Observer, so the non-interactive path pays nothing.
func (r *Runner) streamLine(step domain.StepName, line string) {
	r.notify(StepEvent{Step: step, Phase: StepOutput, Line: line})
}

// withStream sets the step-output sink on sc only when a live Observer is
// attached, so steps stream their output to the UI but stay unbuffered-fast on
// the plain path.
func (r *Runner) withStream(sc StepContext) StepContext {
	if r.Observer != nil {
		sc.Stream = r.streamLine
	}
	return sc
}

// resolvePushGate drives the write-external push action through its approval
// pause: the aggregate decides whether a human is needed, the approver answers,
// and a push failure aborts the run. On success the push executor has already
// performed the real push.
func (r *Runner) resolvePushGate(ctx context.Context, run *domain.Run, kernel Kernel) error {
	// A run cancelled before the gate (Ctrl-C / SIGTERM) must never fall through
	// to the auto-approval path and push. Abort cleanly instead of pushing.
	if err := ctx.Err(); err != nil {
		return run.Abort("run cancelled before push")
	}

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

	// Re-check cancellation right before the irreversible push: on the
	// auto-approval path there is no approver interaction to surface a
	// cancellation, so a Ctrl-C between the gate opening and here would
	// otherwise still push. Reject the pending gate and abort.
	if err := ctx.Err(); err != nil {
		_, _ = kernel.Reject(ctx, gate.SessionID, "warden-cancelled", "run cancelled before push")
		return run.Abort("run cancelled before push")
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
// pushedMessage states what landed where, so "did it push?" is answerable from
// the output alone rather than by running `git ls-remote` — which is what an
// operator had to do while success and failure printed the same thing (#89).
// A delegating run makes no claim: git is about to report that push itself, and
// warden asserting it first would be a second account of the same event, and a
// false one if git's push then fails.
func pushedMessage(sha, remote, branch string, gitCompletes bool) string {
	if gitCompletes {
		return "gate passed; git is completing the push"
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	if short == "" {
		// HEAD was unreadable, so no note was bound either; do not render a blank.
		short = "the gated commit(s)"
	}
	target := branch
	if remote != "" {
		target = remote + "/" + branch
	}
	return fmt.Sprintf("pushed %s to %s; local branch fast-forwarded", short, target)
}

func (r *Runner) result(run *domain.Run, patch string) RunResult {
	return RunResult{
		Outcome:  run.Outcome(),
		Hook:     run.Hook(),
		Policy:   run.Policy(),
		Findings: run.Findings(),
		Record:   run.Record(),
		FixPatch: patch,
		Message:  run.Message(),
		Blocker:  run.Blocker(),
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
