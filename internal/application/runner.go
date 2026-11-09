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

// ErrRewriteUnderAttestOnly is returned when a step rewrote the tree during an
// --attest-only run. The note binds to the repository's HEAD, so attesting a
// rewritten tree would claim the checks passed on a tree they never saw — and
// unlike the normal path there is no branch to carry the fix onto.
var ErrRewriteUnderAttestOnly = errors.New("tree rewritten during an attest-only run")

// ErrAttestationNotWritten is returned when an --attest-only run could not write
// its note.
//
// In the gate path a failed note is best-effort by design: the push already
// happened and provenance is a side-channel (§9). Under --attest-only there is
// no push, so the note is the whole product of the run — swallowing the failure
// reports success for a job that did nothing.
var ErrAttestationNotWritten = errors.New("attestation note could not be written")

// ErrScopeNotAttestable is returned when a run selected an affected scope it
// cannot attest: the tree the per-workspace checks ran on is not the commit's
// tree, or the note could not be signed. An affected-scope note makes a weaker
// claim than a full one, and an unsigned one, or one about a different tree,
// is worth less than none, so the run refuses to write it, before anything is
// pushed.
var ErrScopeNotAttestable = errors.New("affected-scope provenance cannot be attested")

// short12 abbreviates a SHA for a message without pulling in a delivery helper.
func short12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// RunResult is the application's output DTO for a completed run, projected from
// the domain Run aggregate plus delivery-specific extras (the pre-commit fix
// patch). The domain owns the outcome; this is just its read model.
type RunResult struct {
	ReusedSteps map[domain.StepName]domain.ExecutionReuse
	// StepNotes names the steps that passed without executing — cached or
	// skipped — and why. A step absent from it executed. Every surface that
	// lists "the steps that ran" must qualify these, or a skipped review reads
	// as a review that passed. Not part of the signed record.
	StepNotes map[domain.StepName]domain.OutcomeNote
	Outcome   domain.Outcome
	Hook      domain.Hook
	Policy    domain.ResolvedPolicy
	Findings  []domain.Finding
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
	// AttestOnly echoes Settings.AttestOnly so delivery can tell a run that
	// deliberately pushed nothing from one that pushed and needs git to stand
	// down. Without it a passing attest-only run exited 3 — "warden performed the
	// push" — about a commit warden never touched, failing the CI step and
	// leaving the note written but unpublished.
	AttestOnly bool
	Message    string
	// Warnings are non-fatal notices the run wants the developer to see —
	// including provenance publication failures and overrides naming commits
	// force-pushed over. Kept out of Message so the verdict line stays the
	// verdict, and out of the application layer's own stdout so delivery
	// keeps owning I/O.
	Warnings []string
	// Provenance records the observed note publication state, independently of
	// the validation verdict. Empty means publication was not attempted.
	Provenance string
	// PushPerformed distinguishes an already completed push from a pending git
	// push when provenance publication degrades an otherwise passing run.
	PushPerformed bool
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
	// signed. A nil Signer (or a signing failure) leaves the note unsigned, and
	// the run says so — see sign and Config.Signing.
	Signer Signer
	// SBOM is optional: when set, a passing pre-push run records its dependency
	// lockfile digests in the provenance note.
	SBOM SBOM
	// DepDrift is optional: when set, a passing pre-push run records any
	// dependency drift it detected, so the note does not imply the run
	// resolved against the lockfiles it digests (#204).
	DepDrift DepDriftDetector
	// Workspaces is optional: it backs per-workspace steps (per_workspace:).
	// A run whose steps include one fails when it is nil rather than run the
	// step over nothing.
	Workspaces WorkspaceSource
	// BuildCache is optional: when set, a run's steps hold the build cache
	// shared while they use it, and the cache is bounded once the run is over.
	// Nil leaves the cache unguarded and unbounded.
	BuildCache BuildCache
	Settings   Settings
	// Now and NewID are injected for deterministic tests.
	Now   func() time.Time
	NewID func() string
	// Environ is the gating process's environment, read for agent harness
	// markers under a profile (see authorship.go). Nil means os.Environ.
	Environ func() []string
}

// Settings carries run-invariant configuration.
type Settings struct {
	// ExpectedPushBranch and ExpectedPushTip pin Git's hook input across setup.
	ExpectedPushBranch string
	ExpectedPushTip    string
	Version            string
	Remote             string
	// AttestOnly runs the full gate and writes the provenance note, but does not
	// move or push the branch.
	//
	// This is the CI-side mode. warden's gate is client-side pre-push, so a
	// commit the forge creates on its own — a GitHub squash-merge, a web edit, a
	// merged Dependabot PR — can never carry a note: warden was never in that
	// path. Measured across one fleet, every remaining "bypass" was exactly that:
	// eleven commits, all committed by GitHub, none of them a person evading
	// anything.
	//
	// A post-merge CI job closes that gap by running the checks against the
	// merged tree and attesting THAT commit. It must not push: the branch is
	// already published — it is what triggered the job — and a gate that pushed
	// from CI would race the next human push and fail on a stale ref.
	AttestOnly bool
}

// Run executes the pipeline for hook against the repository.
func (r *Runner) Run(ctx context.Context, hook domain.Hook) (RunResult, error) {
	defer r.boundBuildCache()
	return r.run(ctx, hook, false)
}

func (r *Runner) PredictStaged(ctx context.Context) (RunResult, error) {
	defer r.boundBuildCache()
	return r.run(ctx, domain.PreCommit, true)
}

// ErrNoLiveChecks is returned by CheckWorkingTree when the configuration has
// no check the live watcher may run in a container.
var ErrNoLiveChecks = errors.New("no read-only container shell checks to run: configure commands for lint, test or custom steps in steps.pre_commit")

// CheckWorkingTree runs the container-aware live watcher's checks against a
// working-tree snapshot: tree, as `git add -A` would stage the live checkout,
// on top of commit base. It is advisory feedback and nothing more:
//
//   - only container shell checks run (domain.ResolvedPolicy.ContainerShellCheck),
//     in a disposable dependency-free worktree;
//   - no prediction is looked up or saved: the snapshot comes from a different
//     diff source than a commit's, and that equivalence is not proven;
//   - no step is authorized to fix, and no fix patch is computed, so nothing
//     is ever written back to the developer's tree;
//   - nothing is signed, and no note is written.
func (r *Runner) CheckWorkingTree(ctx context.Context, base, tree string) (RunResult, error) {
	defer r.boundBuildCache()
	if !domain.ValidTreeSHA(base) {
		return RunResult{}, errors.New("live checks need a commit to check the working tree against; make the first commit")
	}
	if !domain.ValidTreeSHA(tree) {
		return RunResult{}, fmt.Errorf("live checks: %q is not a tree object name", tree)
	}
	cfg, err := r.Configs.Load()
	if err != nil {
		return RunResult{}, err
	}
	if !cfg.Execution.ContainerEnabled() {
		return RunResult{}, errors.New("live snapshot checks run only under container execution")
	}
	git, ok := r.Git.(DependencyFreeGit)
	if !ok {
		return RunResult{}, errors.New("git adapter cannot create dependency-free container worktrees")
	}
	branch, err := r.Git.CurrentBranch()
	if err != nil {
		return RunResult{}, fmt.Errorf("current branch: %w", err)
	}
	diff, err := git.SnapshotDiffStats(base, tree)
	if err != nil {
		return RunResult{}, err
	}
	risk := cfg.Risk.Thresholds().Classify(diff)
	resolved := policy.Resolve(cfg, policy.Input{Hook: domain.PreCommit, Branch: branch, Paths: diff.Paths, Risk: risk})
	resolved.Commands = cfg.Commands
	resolved.AgentCommands = cfg.AgentCommands
	resolved, err = LiveChecks(resolved)
	if err != nil {
		return RunResult{}, err
	}

	wt, err := git.SeedDependencyFreeSnapshot(base, tree)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed snapshot worktree: %w", err)
	}
	defer func() { _ = wt.Remove() }()
	sc := r.withStream(StepContext{Hook: domain.PreCommit, BuildCacheDir: r.buildCacheDir(), WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands, Remote: r.Settings.Remote})
	run := r.newRun(domain.PreCommit, resolved, branch)
	if _, err := r.runValidation(ctx, run, resolved, sc, nil, wt, nil); err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() {
		return r.result(run, ""), nil
	}
	if err := run.Pass(); err != nil {
		return RunResult{}, err
	}
	return r.result(run, ""), nil
}

// LiveChecks narrows a resolved pre-commit policy to what the live watcher
// runs: its container shell checks, with the predictive cache and every
// auto-fix budget removed, so the kernel neither looks up nor records a
// prediction and no step is authorized to write. It fails with
// ErrNoLiveChecks when nothing is left.
func LiveChecks(resolved domain.ResolvedPolicy) (domain.ResolvedPolicy, error) {
	var selected []domain.StepName
	for _, step := range resolved.Steps {
		if resolved.ContainerShellCheck(step) {
			selected = append(selected, step)
		}
	}
	if len(selected) == 0 {
		return resolved, ErrNoLiveChecks
	}
	resolved.Steps = selected
	resolved.PredictiveCache = nil
	resolved.AutoFix = nil
	return resolved, nil
}

func (r *Runner) run(ctx context.Context, hook domain.Hook, predictOnly bool) (RunResult, error) {
	cfg, err := r.Configs.Load()
	if err != nil {
		return RunResult{}, err
	}
	branch, err := r.Git.CurrentBranch()
	if err != nil {
		return RunResult{}, fmt.Errorf("current branch: %w", err)
	}

	if hook == domain.PrePush && r.Settings.ExpectedPushBranch != "" && branch != r.Settings.ExpectedPushBranch {
		return RunResult{}, fmt.Errorf("%w: checked-out branch changed since Git prepared the push", ErrBranchMoved)
	}
	diff, err := r.diffForRisk(hook, branch, cfg.PR.Base)
	if err != nil {
		return RunResult{}, err
	}
	risk := cfg.Risk.Thresholds().Classify(diff)

	resolved := policy.Resolve(cfg, policy.Input{Hook: hook, Branch: branch, Paths: diff.Paths, Risk: risk})
	resolved.Commands = cfg.Commands
	resolved.AgentCommands = cfg.AgentCommands

	if predictOnly {
		var selected []domain.StepName
		for _, step := range resolved.Steps {
			if resolved.Predictable(step) {
				selected = append(selected, step)
			}
		}
		if len(selected) == 0 {
			return RunResult{}, fmt.Errorf("no eligible predictive_cache checks: configure read-only container shell checks in steps.pre_commit")
		}
		resolved.Steps = selected
	}
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
//
// The no-upstream fallback used to be `base = ""`, described as "diffing
// against the empty base so a first push still gets sensible stats". It did the
// opposite. An empty base selected `git diff --cached --numstat` — the staged
// INDEX — and at pre-push the index is empty, because everything is already
// committed. So the FIRST push of a branch, the one case that fallback existed
// to serve, reported FilesTouched: 0, LinesChanged: 0, Paths: nil.
//
// Two things fail on that, both in silence. No path rule can match an empty
// path set, so every `add:` step is skipped and the push passes looking exactly
// like one that ran them. And Classify reads the same zeroes, so a first push
// is RiskLow however large it is, and an approval gated on high risk never
// fires. In a PR workflow a branch is usually pushed once, so this is the
// common case rather than an edge.
//
// The fallback now resolves a real base, in the order rebase.go already uses
// for the same question (resolveIntegrationBase): where a PR from here would
// go, then the remote's default head. Both name the INTEGRATION point, which is
// what a branch with no upstream should be measured against. The empty tree is
// the last resort — the first branch of a repo with no remote head at all,
// where "everything on HEAD" is the honest answer.
func (r *Runner) diffForRisk(hook domain.Hook, branch, prBase string) (domain.DiffStats, error) {
	if hook == domain.PreCommit {
		return r.Git.StagedDiffStats()
	}
	base, err := r.Git.MergeBase(r.Settings.Remote + "/" + branch)
	if err != nil {
		base, err = r.firstPushBase(prBase)
		if err != nil {
			return domain.DiffStats{}, err
		}
	}
	return r.Git.DiffStats(base)
}

// firstPushBase resolves what to diff against when a branch has no upstream.
//
// It never returns "" with a nil error. An unresolvable base has to fail
// loudly, because the alternative — the one this replaces — is an empty path
// set that quietly disables every path rule in the policy.
func (r *Runner) firstPushBase(prBase string) (string, error) {
	remote := r.Settings.Remote
	if remote == "" {
		remote = "origin"
	}

	// 1. The repo said where PRs from here go; that is the integration point.
	if prBase = strings.TrimSpace(prBase); prBase != "" {
		if base, err := r.Git.MergeBase(remote + "/" + prBase); err == nil && base != "" {
			return base, nil
		}
	}
	// 2. The remote's default head, e.g. origin/HEAD -> origin/main.
	if ref, err := r.Git.DefaultBranchRef(remote); err == nil {
		if ref = strings.TrimSpace(ref); ref != "" {
			if base, err := r.Git.MergeBase(ref); err == nil && base != "" {
				return base, nil
			}
		}
	}
	// 3. No integration point exists — the first branch of a fresh repo. The
	//    empty tree makes base..HEAD mean everything on it.
	return r.Git.EmptyTree()
}

// runPreCommit runs the fast local subset in a worktree seeded from HEAD plus
// staged changes, then reports any fixes to re-apply to the real index (§4.2).
func (r *Runner) runPreCommit(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats) (RunResult, error) {
	wt, err := r.seedWorktree(domain.PreCommit, branch, resolved)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer func() { _ = wt.Remove() }()

	sc := r.withStream(StepContext{Hook: domain.PreCommit, BuildCacheDir: r.buildCacheDir(), WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands, SecurityScan: resolved.SecurityScan, Remote: r.Settings.Remote})
	run := r.newRun(domain.PreCommit, resolved, branch)

	if _, err := r.runValidation(ctx, run, resolved, sc, nil, wt, nil); err != nil {
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

// runPrePush runs the full pipeline in a worktree cloned from the branch tip,
// then fast-forwards back and pushes on approval (§4.3).
func (r *Runner) runPrePush(ctx context.Context, resolved domain.ResolvedPolicy, branch string, diff domain.DiffStats, cfg domain.Config) (res RunResult, runErr error) {
	var pushPerformed bool
	provenance := ""
	defer func() {
		res.PushPerformed = pushPerformed
		res.Provenance = provenance
		res.AttestOnly = r.Settings.AttestOnly
	}()
	prCfg := cfg.PR
	seedTip, err := r.Git.HeadSHA()
	if err != nil {
		return RunResult{}, err
	}
	if r.Settings.ExpectedPushTip != "" && seedTip != r.Settings.ExpectedPushTip {
		return RunResult{}, fmt.Errorf("%w: tip changed since Git prepared the push", ErrBranchMoved)
	}
	wt, err := r.seedWorktree(domain.PrePush, branch, resolved)
	if err != nil {
		return RunResult{}, fmt.Errorf("seed worktree: %w", err)
	}
	defer func() { _ = wt.Remove() }()

	sc := r.withStream(StepContext{Hook: domain.PrePush, BuildCacheDir: r.buildCacheDir(), WorktreeDir: wt.Dir(), Branch: branch, Diff: diff, Commands: resolved.Commands, SecurityScan: resolved.SecurityScan, Remote: r.Settings.Remote, PRBase: cfg.PR.Base, AttestOnly: r.Settings.AttestOnly})
	run := r.newRun(domain.PrePush, resolved, branch)
	if err := r.preflightAuthorship(resolved, branch, seedTip, cfg, wt.Dir()); err != nil {
		return RunResult{}, err
	}

	// spanBase is the commit this push starts from, captured BEFORE the push
	// moves the remote-tracking ref. It becomes the record's CoversFrom, so the
	// note states the span the run vouches for and not just its tip (#86).
	var spanBase string
	// gitCompletes records that warden left the push to git — see delegateToGit.
	var gitCompletes bool
	var pushForce domain.PushForce
	// discardWarning carries a WARDEN_ALLOW_DISCARD override up to the caller so
	// delivery can print which commits were force-pushed over.
	var discardWarning string
	// unsignedWarning carries a silent signing degrade up to the caller. Losing
	// it is how a repo ends up with notes its own CI will later refuse.
	var unsignedWarning string
	// willOpenPR is resolved once, before the gate, because the delegation
	// decision is made inside the push closure but PR creation happens after it.
	willOpenPR := prCfg.Enabled && r.Forge != nil && r.Forge.Available()

	// The push closure runs only after the kernel's approval gate clears. It
	// prepares the branch and push mode. Publication waits for signed evidence.
	push := func(ctx context.Context) (domain.StepResult, error) {
		dirty, err := wt.TrackedDirty()
		if err != nil {
			return domain.StepResult{}, fmt.Errorf("check validated tree: %w", err)
		}
		if dirty {
			return domain.StepResult{}, fmt.Errorf("refusing to attest uncommitted changes in the validated worktree")
		}
		finalSHA, err := wt.HeadSHA()
		if err != nil {
			return domain.StepResult{}, err
		}
		// AttestOnly stops here: the checks have run against this tree and the
		// note is written below, but the branch is already where it belongs. See
		// Settings.AttestOnly — moving or pushing it from CI would race the next
		// human push, and there is nothing to publish that the trigger did not
		// already publish.
		if r.Settings.AttestOnly {
			// …with one refusal. The note is bound to the repository's HEAD, while
			// the checks ran against the worktree. If a step rewrote the tree those
			// are different commits, and writing the note anyway would attest that
			// the checks passed on a tree they never saw — precisely the false claim
			// this whole mode exists to avoid making. There is no branch to push the
			// fix onto here, so the honest outcome is to fail.
			repoHead, headErr := r.Git.HeadSHA()
			if headErr == nil && repoHead != finalSHA {
				return domain.StepResult{}, fmt.Errorf(
					"%w: a step rewrote the tree (checked %s, HEAD is %s); "+
						"--attest-only cannot attest a tree that is not the commit's",
					ErrRewriteUnderAttestOnly, short12(finalSHA), short12(repoHead))
			}
			return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
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
		pushForce = force
		return domain.StepResult{Step: domain.StepPush, Status: domain.StepPass}, nil
	}

	// sels carries the workspace selections of the run's per-workspace steps,
	// and what its signed scope is built from. An anchor for an affected
	// selection must be signed by the committed roster, or without one by
	// this machine's own signer.
	sels := newWorkspaceSelections()
	sels.trustedKeys = cfg.TrustedKeys
	kernel, err := r.runValidation(ctx, run, resolved, sc, push, wt, sels)
	if err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() { // a validation step failed
		return r.result(run, ""), nil
	}

	approval, err := r.resolvePushGate(ctx, run, kernel)
	if err != nil {
		return RunResult{}, err
	}
	if run.IsTerminal() { // rejected or aborted at the gate
		return r.result(run, ""), nil
	}

	// Provenance: verify the run-level evidence chain and write the note.
	record, err := r.buildRecord(kernel, run, approval)
	if err != nil {
		return RunResult{}, err
	}
	// Bind the record to the commit it attests BEFORE signing, so the commit SHA
	// is covered by the signature and the note can't be transplanted to another
	// commit. The same SHA is the note key. If HEAD can't be read the record stays
	// unbound and no note is written (best-effort provenance never fails the gate).
	finalSHA, shaErr := r.Git.HeadSHA()
	// Attach the SBOM before signing so the dependency digests are covered by the
	// signature. It is read from finalSHA's tree — the commit the note attests —
	// and not from the worktree, where a step may have generated or rewritten a
	// lockfile that commit does not contain. No commit, no claim. Best-effort: a
	// collector that finds nothing leaves it empty.
	if r.SBOM != nil && shaErr == nil {
		record.Dependencies = r.SBOM.Collect(sc.WorktreeDir, finalSHA)
	}
	// Attach drift in the same place and for the same reasons: it must be
	// inside the signature (a caveat the verifier cannot see is not a caveat),
	// and it compares finalSHA's committed lockfiles, not the worktree's.
	if r.DepDrift != nil && shaErr == nil {
		record.DependenciesDrifted = r.DepDrift.DetectDepDrift(sc.WorktreeDir, finalSHA)
	}
	if shaErr == nil {
		record.CommitSHA = finalSHA
		// Claim the span only when it is strictly below the tip. A base equal to
		// (or absent from) the tip covers nothing extra, and claiming an empty or
		// self-referential span would be noise in a signed statement.
		if spanBase != "" && spanBase != finalSHA {
			record.CoversFrom = spanBase
		}
		// Record which workspaces the per-workspace steps checked, inside the
		// signature (design 0006 §4). A run with no scope to record leaves the
		// note byte-identical to one written before scopes existed.
		if err := r.attachScope(record, sels, resolved, finalSHA); err != nil {
			return r.result(run, ""), err
		}
	}
	// Notarize BEFORE signing, so the trace digest is inside the payload the
	// signature covers. Bound alongside it instead, the digest would be as
	// editable as the trace it vouches for.
	if traceErr := r.notarizeAgentTrace(record, cfg, sc.WorktreeDir); traceErr != nil {
		return r.result(run, ""), traceErr
	}
	// Bind the observed agent declarations next to the trace they may require,
	// inside the signature for the same reason.
	if err := r.bindAuthorship(record, resolved, cfg); err != nil {
		return r.result(run, ""), err
	}
	// Signing degrades silently by design (see sign), which is why the reason is
	// captured and surfaced rather than discarded. A repo that never learns its
	// notes stopped being signed discovers it only when a CI --require-signed
	// starts failing, by which point the commits are already in history.
	if reason := r.sign(record); reason != "" {
		// An affected scope was only selected because a signer was available
		// (see selectAtPrePush). If signing failed after all, the partial
		// claim cannot be told from a hand-edited one: refuse it whatever
		// signing.required says.
		if !record.FullScope() {
			return r.result(run, ""), fmt.Errorf(
				"%w: the note could not be signed (%s), and an unsigned affected-scope note proves nothing; push again, and the run will check every workspace if no signer is available",
				ErrScopeNotAttestable, reason)
		}
		if cfg.Signing.Required {
			// signing.required is the repo saying an unsigned note is not an
			// acceptable outcome. Abort rather than write one — the push has not
			// happened yet at this point, so refusing is still cheap.
			return r.result(run, ""), fmt.Errorf(
				"refusing to write an unsigned provenance note: %s (signing.required is set in .warden.yaml)", reason)
		}
		unsignedWarning = "provenance note written UNSIGNED: " + reason +
			". It still proves the checks ran, but not WHO ran them — `warden verify --require-signed` will reject it. Set signing.required to fail instead."
	}
	// Signing and trace validation must succeed before any branch publication.
	if !gitCompletes && !r.Settings.AttestOnly {
		if err := r.Git.Push(r.Settings.Remote, branch, pushForce); err != nil {
			return r.result(run, ""), fmt.Errorf("push: %w", err)
		}
		pushPerformed = true
	}
	var noteErr error
	var notePushWarning string
	provenance = "missing"
	// noteWriteWarning covers the failure one step earlier than notePushWarning:
	// no note was written AT ALL, not even locally. Checks remain passed, but
	// delivery reports degraded publication and stops any pending git push.
	// A push already performed cannot be undone. `git notes add` needs a
	// committer identity and fails outright without one, which is
	// how an --attest-only run once reported "attested" having written nothing
	// (#183). The gate path had the same hole, minus the exit code.
	var noteWriteWarning string
	if shaErr != nil {
		noteWriteWarning = "no provenance note was written: HEAD could not be read (" + shaErr.Error() +
			"), so there was no commit to bind the record to. This commit will read as ungated everywhere."
	}
	if shaErr == nil {
		// Record publication independently: validation passed even if this write
		// fails, and the branch may already have been pushed.
		if noteErr = r.Git.WriteNote(finalSHA, *record); noteErr != nil {
			noteWriteWarning = "provenance note could NOT be written: " + noteErr.Error() +
				". This commit carries no provenance and will read as an " +
				"ungated bypass everywhere — including in the CI gate."
		} else {
			provenance = "local"
			// Anchor before publishing: the note is worthless for re-attestation if
			// the commit it annotates gets collected once the branch goes away, and
			// `--delete-branch` is the default merge flow (#212 §3). Silent on
			// failure — this is a durability hint, and the run has already pushed.
			_ = r.Git.AnchorAttested(finalSHA)
			// A publication failure is degraded success. A note that reaches no remote is
			// provenance nobody else can use: the commit verifies on this machine
			// and reads as an ungated bypass everywhere else, including in the CI
			// gate — which then accuses the author of something they did not do.
			// Staying quiet about that was survivable while a developer's machine
			// was the only writer of refs/notes/warden; CI is a second one now
			// (#186).
			if err := r.Git.PushNotes(r.Settings.Remote); err != nil {
				notePushWarning = "provenance note written locally but NOT published: " + err.Error() +
					". This commit will read as ungated to everyone else until the note reaches the remote."
			} else {
				provenance = "published"
			}
		}
	}
	// …but under --attest-only the note is the ENTIRE product of the run. There is
	// no push, so a swallowed write failure means the job did nothing and said it
	// succeeded — which is exactly what happened: `git notes add` needs a
	// committer identity, CI checkouts have none, and warden reported
	//
	//   gate passed; attested b27a99d471cc
	//
	// then exited 0 with no note anywhere. A green run that attested nothing is
	// worse than a red one, because it stops anybody looking.
	if r.Settings.AttestOnly {
		if shaErr != nil {
			return r.result(run, ""), fmt.Errorf("%w: cannot read HEAD to bind the note to", ErrAttestationNotWritten)
		}
		if noteErr != nil {
			return r.result(run, ""), fmt.Errorf("%w: %v", ErrAttestationNotWritten, noteErr)
		}
	}
	msg := pushedMessage(finalSHA, r.Settings.Remote, branch, gitCompletes, r.Settings.AttestOnly)
	if sum := record.Scope.Summary(); sum != "" {
		msg += "; scope " + sum
	}
	if provenance != "published" {
		if gitCompletes {
			msg = "checks passed; provenance incomplete; git push stopped before publication"
		} else {
			msg += "; provenance incomplete"
		}
	}
	if err := run.MarkPushed(*record, msg); err != nil {
		return RunResult{}, err
	}

	res = r.result(run, "")
	res.GitCompletesPush = gitCompletes
	res.AttestOnly = r.Settings.AttestOnly
	res.Provenance = provenance
	if discardWarning != "" {
		res.Warnings = append(res.Warnings, discardWarning)
	}
	// The unsigned warning says the note "was written UNSIGNED". When no note was
	// written it is a false statement about the artifact, and the louder failure
	// is the missing note, not its missing signature — so the two are mutually
	// exclusive rather than stacked.
	if noteWriteWarning != "" {
		res.Warnings = append(res.Warnings, noteWriteWarning)
	} else if unsignedWarning != "" {
		res.Warnings = append(res.Warnings, unsignedWarning)
	}
	if notePushWarning != "" {
		res.Warnings = append(res.Warnings, notePushWarning)
	}
	// Publishing the verdict is best-effort and post-push for the same reason
	// as PR creation, and matters most where CI cannot run: without it the
	// commit is gated but looks ungated to branch protection.
	if provenance == "published" && cfg.Status.IsEnabled() && r.Forge != nil && r.Forge.Available() && finalSHA != "" {
		// The forge refuses a status for a commit it has never seen, and when
		// git is completing the push the branch has not reached it yet. Put the
		// commit there under its anchor ref first — explicitly, and checked,
		// rather than relying on the bulk anchor push that rides along with the
		// notes and is silent about failing.
		if err := r.Git.PushAnchor(r.Settings.Remote, finalSHA); err != nil {
			res.Warnings = append(res.Warnings,
				"gate passed but its commit status was NOT published: the commit could not be made reachable on "+
					r.Settings.Remote+" ("+err.Error()+"). This commit will read as ungated to branch protection until it is.")
		} else if err := r.Forge.PublishStatus(ctx, finalSHA, "success",
			cfg.Status.StatusContext(), statusDescription(run, record)); err != nil {
			res.Warnings = append(res.Warnings,
				"gate passed but its commit status was NOT published: "+err.Error()+
					". This commit will read as ungated to branch protection until it is.")
		}
	}
	// PR creation is best-effort and post-push: a forge failure never unwinds a
	// push that already succeeded (§4.3). Only run it when enabled and usable.
	if willOpenPR && provenance == "published" {
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

// resolvePushGate drives the write-external push action through its approval
// pause: the aggregate decides whether a human is needed, the approver answers,
// and a push failure aborts the run. On success the push executor has already
// performed the real push.
//
// The returned ApprovalRef records how the gate was answered, for the signed
// record of a profiled run. It is meaningful only when the error is nil and
// the run is not terminal.
func (r *Runner) resolvePushGate(ctx context.Context, run *domain.Run, kernel Kernel) (domain.ApprovalRef, error) {
	// No decision was required unless an approver is consulted below.
	approval := domain.ApprovalRef{Channel: domain.ApprovalNone}
	// A run cancelled before the gate (Ctrl-C / SIGTERM) must never fall through
	// to the auto-approval path and push. Abort cleanly instead of pushing.
	if err := ctx.Err(); err != nil {
		return approval, run.Abort("run cancelled before push")
	}

	gate, err := kernel.Execute(ctx, domain.StepPush)
	if err != nil {
		return approval, err
	}
	if !gate.NeedsApproval {
		return approval, nil
	}

	decision := autoApproval()
	if run.RequiresApproval() {
		decision, err = r.Approver.Approve(ctx, ApprovalRequest{
			Hook: run.Hook(), Branch: run.Branch(), Steps: run.Policy().Steps, Findings: run.Findings(), Risk: run.Policy().Risk,
			Profile: run.Policy().Profile,
		})
		if err != nil {
			return approval, err
		}
		approval = decision.approvalRef()
	}
	if decision.NeedsHuman {
		_, _ = kernel.Reject(ctx, gate.SessionID, decision.Principal, decision.Rationale)
		return approval, run.AwaitHuman(decision.Rationale)
	}
	if !decision.Approved {
		_, _ = kernel.Reject(ctx, gate.SessionID, decision.Principal, decision.Rationale)
		return approval, run.Reject("approval declined")
	}

	// Re-check cancellation right before the irreversible push: on the
	// auto-approval path there is no approver interaction to surface a
	// cancellation, so a Ctrl-C between the gate opening and here would
	// otherwise still push. Reject the pending gate and abort.
	if err := ctx.Err(); err != nil {
		_, _ = kernel.Reject(ctx, gate.SessionID, "warden-cancelled", "run cancelled before push")
		return approval, run.Abort("run cancelled before push")
	}

	if _, err := kernel.Approve(ctx, gate.SessionID, decision.Principal, decision.Rationale); err != nil {
		// The push executor's failure crosses the axi boundary as a message, so
		// branch-moved is matched by substring as well as errors.Is. Either way
		// a failed push aborts — never a successful gate.
		if errors.Is(err, ErrBranchMoved) || strings.Contains(err.Error(), ErrBranchMoved.Error()) {
			return approval, run.Abort("branch changed mid-run; re-push")
		}
		return approval, run.Abort("push failed: " + err.Error())
	}
	return approval, nil
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
		ReusedSteps: run.ExecutionReuses(),
		StepNotes:   run.StepNotes(),
		Outcome:     run.Outcome(),
		Hook:        run.Hook(),
		Policy:      run.Policy(),
		Findings:    run.Findings(),
		Record:      run.Record(),
		FixPatch:    patch,
		Message:     run.Message(),
		Blocker:     run.Blocker(),
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

func (r *Runner) seedWorktree(hook domain.Hook, branch string, p domain.ResolvedPolicy) (Worktree, error) {
	if p.Execution.ContainerEnabled() {
		git, ok := r.Git.(DependencyFreeGit)
		if !ok {
			return nil, fmt.Errorf("git adapter cannot create dependency-free container worktrees")
		}
		return git.SeedDependencyFreeWorktree(hook, branch)
	}
	if hook == domain.PreCommit {
		return r.Git.SeedWorktreeFromHead(p.MaterializeDeps)
	}
	return r.Git.SeedWorktreeFromBranch(branch, p.MaterializeDeps)
}
