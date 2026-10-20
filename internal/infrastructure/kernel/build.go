package kernel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"go.klarlabs.de/axi"
	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
)

// actionRef is the executor binding ref for a step action.
func actionRef(s domain.StepName) axidomain.ActionExecutorRef {
	return axidomain.ActionExecutorRef("exec." + string(s))
}

// PushFunc performs the real fast-forward-back, origin push, and note write
// once the terminal push action is approved (§4.3). Returning git.ErrBranchMoved
// signals a mid-run conflict that aborts the run without mutating state.
type PushFunc func(ctx context.Context) (domain.StepResult, error)

// stepEffect maps a step to its axi effect level. Validation steps run
// write-local (they mutate only the disposable worktree, no pause); the
// terminal push is write-external so the kernel pauses it at awaiting_approval
// — the run-level approval point, reached only after every validation step has
// already produced its findings (§4.4, §5.4).
func stepEffect(s domain.StepName, policy domain.ResolvedPolicy) axidomain.EffectLevel {
	switch {
	case s == domain.StepPush:
		return axidomain.EffectWriteExternal
	case policy.WritesTree(s):
		return axidomain.EffectWriteLocal
	default:
		return axidomain.EffectReadLocal
	}
}

// Build constructs a fresh in-memory kernel for one run and registers an action
// per resolved step, each bound to an executor closing over the run's context.
// Custom (non-built-in) steps are bound to a SubprocessStep. When push is
// non-nil a terminal write-external push action is also registered.
//
// A new kernel per run is deliberate: it is cheap (in-memory adapters) and lets
// per-step agent/budget/effect selection be baked into the closures without any
// cross-run state to reset.
func Build(reg application.Registry, policy domain.ResolvedPolicy, sc application.StepContext, priors *[]domain.Finding, push PushFunc, cache application.StepCache) (*axi.Kernel, error) {
	container, err := steps.ResolveContainer(policy.Execution)
	if err != nil {
		return nil, err
	}
	// Remove what killed runs left on the engine this run uses.
	steps.SweepSelectedInBackground(container)
	sc.Container = container

	var actions []*axidomain.ActionDefinition
	execs := map[axidomain.ActionExecutorRef]axidomain.ActionExecutor{}

	// One mutex shared by every executor guards the run's accumulating findings,
	// so steps in a parallel batch never race on the priors slice.
	priorsMu := &sync.Mutex{}

	// One pool for every per-workspace step of the run, so fan-out inside a
	// parallel batch shares a single bound instead of multiplying it.
	pool := steps.NewWorkspacePool(workspaceConcurrency(policy))

	for _, name := range policy.Steps {
		step, err := resolveStep(reg, name, policy, pool)
		if err != nil {
			return nil, err
		}
		_, perWorkspace := step.(steps.WorkspaceStep)
		stepSC := sc
		// Commands are sourced from the resolved policy so a config-command
		// custom step (and the built-in shell steps) always see them, regardless
		// of what the caller seeded into sc.
		stepSC.Commands = policy.Commands
		if _, shell := step.(steps.ShellStep); !shell || !policy.Predictable(name) {
			stepSC.Predictions = nil
		}
		stepSC.ConfigurationDigest = policy.ConfigurationDigest
		stepSC.Agent = policy.AgentFor(name)
		stepSC.AgentCommand = domain.ResolveAgentCommand(policy.AgentCommands, stepSC.Agent)
		stepSC.AgentBoundary = policy.AgentBoundaryFor(name)
		stepSC.AutoFixBudget = policy.AutoFixBudget(name)
		stepSC.Timeout = policy.TimeoutFor(name)
		stepSC.Diagnostics = policy.Diagnostics[name]
		// Bind the run's step-tagged output sink into this step's OnOutput, so a
		// step streams live output without knowing its own name plumbing.
		if sc.Stream != nil {
			name := name
			stepSC.OnOutput = func(line string) { sc.Stream(name, line) }
		}

		// Wrap a cacheable, non-mutating step so an unchanged run is skipped. The
		// command signature (shell + agent) is part of the key, so changing what a
		// step runs busts its cache.
		// A per-workspace step is never cached: its inputs are the selection
		// as well as the files, and config load refuses a cache: entry for it.
		if cache != nil && sc.Container == nil && !perWorkspace && policy.Cacheable(name) {
			cmdSig := policy.Commands[string(name)] + "\x00" + stepSC.AgentCommand
			step = cachedStep{inner: step, cache: cache, globs: policy.CachePaths(name), command: cmdSig}
		}

		def, err := newStepAction(name, policy)
		if err != nil {
			return nil, err
		}
		actions = append(actions, def)
		execs[actionRef(name)] = stepExecutor{step: step, sc: stepSC, priors: priors, priorsMu: priorsMu}
	}

	if push != nil {
		def, err := newStepAction(domain.StepPush, policy)
		if err != nil {
			return nil, err
		}
		actions = append(actions, def)
		execs[actionRef(domain.StepPush)] = pushExecutor{push: push}
	}

	contribution, err := axidomain.NewPluginContribution("warden.steps", actions, nil)
	if err != nil {
		return nil, fmt.Errorf("build contribution: %w", err)
	}
	bundle, err := axidomain.NewPluginBundle(contribution, execs, nil)
	if err != nil {
		return nil, fmt.Errorf("build bundle: %w", err)
	}

	k := axi.New()
	if err := k.RegisterBundle(bundle); err != nil {
		return nil, fmt.Errorf("register bundle: %w", err)
	}
	return k, nil
}

// resolveStep finds a step's implementation, in order of preference:
//  1. a per-workspace step — a step defined under per_workspace: fans out
//     over the run's workspace selection, even when it shares a built-in's
//     name (lint, test): the config says it runs per workspace;
//  2. a registered built-in step;
//  3. a config-defined command step — a custom step whose name has a
//     commands.<name> entry runs that shell command, no code required;
//  4. a subprocess step — a warden-step-<name> binary on PATH speaking the
//     stepsdk wire protocol, for steps that need structured findings/approval.
//
// The command path (3) is the easy default: a repo adds a custom check by
// naming a command, not by writing and installing a Go binary. The subprocess
// path (4) stays as the advanced escape hatch.
func resolveStep(reg application.Registry, name domain.StepName, policy domain.ResolvedPolicy, pool *steps.WorkspacePool) (application.Step, error) {
	if ws := policy.Workspaces; ws != nil {
		if def, ok := ws.Steps[name]; ok {
			return steps.NewWorkspaceStep(name, def, pool, policy.KeepsWrites(name)), nil
		}
	}
	if step, ok := reg[name]; ok {
		return step, nil
	}
	if name.IsBuiltin() {
		return nil, fmt.Errorf("built-in step %q has no registered implementation", name)
	}
	if cmd, ok := policy.Commands[string(name)]; ok && strings.TrimSpace(cmd) != "" {
		return commandStep(name), nil
	}
	bin, err := exec.LookPath(customStepBinary(name))
	if err != nil {
		return nil, fmt.Errorf("custom step %q: define commands.%s or per_workspace.%s in .warden.yaml, or install a %s binary on PATH: %w",
			name, name, name, customStepBinary(name), err)
	}
	return NewSubprocessStep(name, bin), nil
}

// workspaceConcurrency is the run's per-workspace pool size: the configured
// workspaces.concurrency, or 0 for the default.
func workspaceConcurrency(p domain.ResolvedPolicy) int {
	if p.Workspaces == nil {
		return 0
	}
	return p.Workspaces.Concurrency
}

// commandStep picks the implementation for a config-defined command step. Every
// step runs its command verbatim; `security-scan` gets a wrapper that also
// reads the scanner's report, so it can fail on the findings the change
// introduced instead of on the repo's whole backlog. The wrapper falls back to
// the plain shell behavior for any command whose output it cannot read, so a
// `make audit` or `npm audit` scan step is unaffected.
func commandStep(name domain.StepName) application.Step {
	if name == domain.StepSecurityScan {
		return steps.NewSecurityScanStep(name)
	}
	return steps.NewShellStep(name, string(name))
}

// newStepAction builds the axi ActionDefinition for a step: empty contracts
// (step I/O flows through the executor closure, not the kernel's typed input),
// the step's effect level, and a non-idempotent profile.
func newStepAction(name domain.StepName, policy domain.ResolvedPolicy) (*axidomain.ActionDefinition, error) {
	def, err := axidomain.NewActionDefinition(
		axidomain.ActionName(name),
		fmt.Sprintf("warden %s step", name),
		axidomain.EmptyContract(),
		axidomain.EmptyContract(),
		nil,
		axidomain.EffectProfile{Level: stepEffect(name, policy)},
		axidomain.IdempotencyProfile{IsIdempotent: false},
	)
	if err != nil {
		return nil, fmt.Errorf("define action %q: %w", name, err)
	}
	if err := def.BindExecutor(actionRef(name)); err != nil {
		return nil, fmt.Errorf("bind action %q: %w", name, err)
	}
	return def, nil
}

// pushExecutor runs the runner-supplied PushFunc when the push action is
// approved, surfacing its StepResult and evidence.
type pushExecutor struct {
	push PushFunc
}

func (e pushExecutor) Execute(ctx context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	res, err := e.push(ctx)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{
		Data:    map[string]any{resultKey: res},
		Summary: res.Summary,
	}, evidenceFor(res), nil
}
