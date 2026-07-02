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

	// One mutex shared by every executor guards the run's accumulating findings,
	// so steps in a parallel batch never race on the priors slice.
	priorsMu := &sync.Mutex{}

	for _, name := range policy.Steps {
		step, err := resolveStep(reg, name, policy.Commands)
		if err != nil {
			return nil, err
		}
		stepSC := sc
		// Commands are sourced from the resolved policy so a config-command
		// custom step (and the built-in shell steps) always see them, regardless
		// of what the caller seeded into sc.
		stepSC.Commands = policy.Commands
		stepSC.Agent = policy.AgentFor(name)
		stepSC.AgentCommand = domain.ResolveAgentCommand(policy.AgentCommands, stepSC.Agent)
		stepSC.AutoFixBudget = policy.AutoFixBudget(name)

		def, err := newStepAction(name)
		if err != nil {
			return nil, err
		}
		actions = append(actions, def)
		execs[actionRef(name)] = stepExecutor{step: step, sc: stepSC, priors: priors, priorsMu: priorsMu}
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

// resolveStep finds a step's implementation, in order of preference:
//  1. a registered built-in step;
//  2. a config-defined command step — a custom step whose name has a
//     commands.<name> entry runs that shell command, no code required;
//  3. a subprocess step — a warden-step-<name> binary on PATH speaking the
//     stepsdk wire protocol, for steps that need structured findings/approval.
//
// The command path (2) is the easy default: a repo adds a custom check by
// naming a command, not by writing and installing a Go binary. The subprocess
// path (3) stays as the advanced escape hatch.
func resolveStep(reg application.Registry, name domain.StepName, commands map[string]string) (application.Step, error) {
	if step, ok := reg[name]; ok {
		return step, nil
	}
	if name.IsBuiltin() {
		return nil, fmt.Errorf("built-in step %q has no registered implementation", name)
	}
	if cmd, ok := commands[string(name)]; ok && strings.TrimSpace(cmd) != "" {
		return steps.NewShellStep(name, string(name)), nil
	}
	bin, err := exec.LookPath(customStepBinary(name))
	if err != nil {
		return nil, fmt.Errorf("custom step %q: define commands.%s in .warden.yaml, or install a %s binary on PATH: %w",
			name, name, customStepBinary(name), err)
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
