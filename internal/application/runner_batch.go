package application

import (
	"context"
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// runValidation builds the run's kernel and folds each resolved step's outcome
// into the aggregate, stopping as soon as the aggregate reaches a terminal
// state. Independent (read-only) steps run concurrently in batches; steps that
// write the worktree stay sequential barriers (see scheduleBatches). It returns
// the kernel so the caller can resolve the push gate.
func (r *Runner) runValidation(ctx context.Context, run *domain.Run, resolved domain.ResolvedPolicy, sc StepContext, push PushFunc, wt Worktree, sels *workspaceSelections) (Kernel, error) {
	// Per-step worktree isolation: steps in a parallel batch each run in their own
	// ephemeral worktree cloned from the canonical one, so a step's writes can't
	// race a sibling. The registry maps a step to its worktree during a batch; an
	// unregistered step (a sequential barrier) falls back to the canonical wt.
	reg := newWorktreeRegistry()
	sc.WorktreeFor = reg.dirFor
	if sels == nil {
		sels = newWorkspaceSelections()
	}
	sc.Selection = sels.get
	release := r.holdBuildCache(&sc)
	defer release()

	var priors []domain.Finding
	kernel, err := r.Kernels.New(resolved, sc, &priors, push)
	if err != nil {
		return nil, err
	}
	for _, batch := range resolved.Batches() {
		if err := r.prepareWorkspaces(sels, sc.Hook, resolved, batch, wt); err != nil {
			return nil, err
		}
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
	// Agent steps a compliance profile isolates get their own guarded clone in
	// either path below; without a profile this clones and captures nothing.
	iso, failed, err := isolateAgents(batch, wt, reg, resolved)
	defer func() {
		iso.cleanup()
		reg.reset()
	}()
	if err != nil {
		return err
	}
	if failed != nil {
		r.notify(StepEvent{Step: failed.Step, Phase: StepStarted})
		if err := run.RecordStep(*failed); err != nil {
			return err
		}
		r.notify(StepEvent{Step: failed.Step, Phase: StepFinished, Result: *failed})
		return nil
	}

	if len(batch) == 1 {
		step := batch[0]
		r.notify(StepEvent{Step: step, Phase: StepStarted})
		out, err := kernel.Execute(ctx, step)
		if err != nil {
			return fmt.Errorf("step %s: %w", step, err)
		}
		out.Result = iso.checkClone(step, out.Result)
		if out.Result, err = iso.checkCanonical(step, out.Result); err != nil {
			return err
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
	// worktree — except for an agent step a profile isolates, which isolateAgents
	// already cloned and fails rather than falls back. All clones are torn down
	// and the registry cleared after the batch, discarding the steps'
	// side-effects.
	// Container checks also install platform-specific untracked dependencies, so
	// isolate every parallel check and fail if that isolation cannot be created.
	if wt != nil {
		var clones []Worktree
		defer func() {
			for _, c := range clones {
				_ = c.Remove()
			}
		}()
		for _, step := range batch {
			if iso.isolated(step) {
				continue // already in its own guarded clone
			}
			if !resolved.WritesTree(step) && !resolved.Execution.ContainerEnabled() {
				continue // host read-only: share the canonical worktree, no clone
			}
			clone, err := wt.Clone(resolved.MaterializeDeps)
			if err != nil {
				if resolved.Execution.ContainerEnabled() {
					return fmt.Errorf("isolate container step %s: %w", step, err)
				}
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
		if iso.isolated(step) {
			// Reported below, once the validated worktree has been compared
			// after the whole batch: until then its verdict is not known.
			iso.checkClone(step, out.Result)
			return
		}
		r.notify(StepEvent{Step: step, Phase: StepFinished, Result: out.Result})
	}
	outcomes, err := kernel.ExecuteBatch(ctx, batch, onFinish)
	if err != nil {
		return err
	}
	for i := range outcomes {
		step := outcomes[i].Result.Step
		if step == "" && i < len(batch) {
			step = batch[i]
		}
		if !iso.isolated(step) {
			continue
		}
		outcomes[i].Result = iso.checkClone(step, outcomes[i].Result)
		if outcomes[i].Result, err = iso.checkCanonical(step, outcomes[i].Result); err != nil {
			return err
		}
		r.notify(StepEvent{Step: step, Phase: StepFinished, Result: outcomes[i].Result})
	}
	// Fold outcomes in declared order. A failing step terminates the run, so
	// stop before recording into an already-terminal run — otherwise a second
	// failing/any step in the same parallel batch surfaces the opaque
	// "record step X: run is already terminal" instead of the real failure.
	for i := range outcomes {
		if run.IsTerminal() {
			break
		}
		if err := run.RecordStep(outcomes[i].Result); err != nil {
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
