package kernel

import (
	"context"
	"fmt"
	"os/exec"

	"go.klarlabs.de/axi"
	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
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
func stepEffect(s domain.StepName) axidomain.EffectLevel {
	switch s {
	case domain.StepIntent, domain.StepTest:
		return axidomain.EffectReadLocal
	case domain.StepPush:
		return axidomain.EffectWriteExternal
	default:
		return axidomain.EffectWriteLocal
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
func Build(reg application.Registry, policy domain.ResolvedPolicy, sc application.StepContext, priors *[]domain.Finding, push PushFunc) (*axi.Kernel, error) {
	var actions []*axidomain.ActionDefinition
	execs := map[axidomain.ActionExecutorRef]axidomain.ActionExecutor{}

	for _, name := range policy.Steps {
		step, err := resolveStep(reg, name)
		if err != nil {
			return nil, err
		}
		stepSC := sc
		stepSC.Agent = policy.AgentFor(name)
		stepSC.AgentCommand = policy.AgentCommands[stepSC.Agent]
		stepSC.AutoFixBudget = policy.AutoFixBudget(name)

		def, err := newStepAction(name)
		if err != nil {
			return nil, err
		}
		actions = append(actions, def)
		execs[actionRef(name)] = stepExecutor{step: step, sc: stepSC, priors: priors}
	}

	if push != nil {
		def, err := newStepAction(domain.StepPush)
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

// resolveStep finds a step's implementation: a registered built-in, or a
// subprocess adapter for a custom step resolved by convention on PATH.
func resolveStep(reg application.Registry, name domain.StepName) (application.Step, error) {
	if step, ok := reg[name]; ok {
		return step, nil
	}
	if name.IsBuiltin() {
		return nil, fmt.Errorf("built-in step %q has no registered implementation", name)
	}
	bin, err := exec.LookPath(customStepBinary(name))
	if err != nil {
		return nil, fmt.Errorf("custom step %q: binary %s not found on PATH: %w", name, customStepBinary(name), err)
	}
	return NewSubprocessStep(name, bin), nil
}

// newStepAction builds the axi ActionDefinition for a step: empty contracts
// (step I/O flows through the executor closure, not the kernel's typed input),
// the step's effect level, and a non-idempotent profile.
func newStepAction(name domain.StepName) (*axidomain.ActionDefinition, error) {
	def, err := axidomain.NewActionDefinition(
		axidomain.ActionName(name),
		fmt.Sprintf("warden %s step", name),
		axidomain.EmptyContract(),
		axidomain.EmptyContract(),
		nil,
		axidomain.EffectProfile{Level: stepEffect(name)},
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
