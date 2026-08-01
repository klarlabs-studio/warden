package cli

import (
	"context"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
	"go.klarlabs.de/warden/internal/service"
)

// facade adapts *service.Service to mcpserver.Facade, the operation set the axi
// and MCP surfaces share. Keeping it here means the service stays free of any
// delivery-layer type.
type facade struct{ svc *service.Service }

func (f facade) PolicyExplain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error) {
	return f.svc.Explain(hook, branch, paths)
}

func (f facade) StepsList() (preCommit, prePush []domain.StepName, err error) {
	return f.svc.StepsList()
}

func (f facade) RunTrigger(ctx context.Context, hook domain.Hook) (mcpserver.RunSummary, error) {
	return f.runTrigger(ctx, hook, nil)
}

// RunTriggerStreaming runs the pipeline and reports each step as it finishes.
//
// It installs a step observer for the duration of the run. That is safe here
// because the facade owns its own service instance — newFacade constructs one
// per surface invocation — so an observer set for one run cannot leak into
// another's reporting.
func (f facade) RunTriggerStreaming(ctx context.Context, hook domain.Hook, onStep func(mcpserver.StepProgress)) (mcpserver.RunSummary, error) {
	return f.runTrigger(ctx, hook, onStep)
}

// stepObserver adapts the application's step events to the agent surface's
// progress callback. It reports only FINISHED steps: the output-line events are
// documented as best-effort and droppable under load, so forwarding them would
// hand a polling caller a partial transcript it could mistake for a complete one.
type stepObserver struct{ onStep func(mcpserver.StepProgress) }

func (o stepObserver) OnStep(e application.StepEvent) {
	if e.Phase != application.StepFinished {
		return
	}
	o.onStep(mcpserver.StepProgress{
		Step:     e.Step,
		Status:   string(e.Result.Status),
		Summary:  e.Result.Summary,
		Findings: e.Result.Findings,
	})
}

func (f facade) runTrigger(ctx context.Context, hook domain.Hook, onStep func(mcpserver.StepProgress)) (mcpserver.RunSummary, error) {
	if onStep != nil {
		f.svc.SetObserver(stepObserver{onStep: onStep})
	}
	res, err := f.svc.Run(ctx, hook)
	if err != nil {
		return mcpserver.RunSummary{}, err
	}
	summary := mcpserver.RunSummary{
		Outcome:  string(res.Outcome),
		Hook:     string(res.Hook),
		Steps:    res.Policy.Steps,
		Findings: res.Findings,
		Message:  res.Message,
		// Carry the blocker through: the run already decided whether the
		// obstacle was the machine or the change, and an agent needs that
		// verdict as data, not prose.
		Blocker:   string(res.Blocker),
		Retryable: res.Blocker.Retryable(),
	}
	if res.Record != nil {
		summary.RunID = res.Record.RunID
	}
	return summary, nil
}

// Verify adapts the service's verify result onto the delivery-neutral shape.
// trustedKeys is variadic on the service but a slice here, because an agent's
// argument schema carries it as one.
func (f facade) Verify(commitish string, trustedKeys []string) (mcpserver.ProvenanceRecord, error) {
	res, err := f.svc.Verify(commitish, trustedKeys...)
	if err != nil {
		return mcpserver.ProvenanceRecord{}, err
	}
	return mcpserver.ProvenanceRecord{
		Validated:      res.Validated,
		Signed:         res.Signed,
		SignatureValid: res.SignatureValid,
		Signer:         res.Signer,
		Trusted:        res.Trusted,
		Record:         res.Record,
	}, nil
}

// VerifyRange gates a whole span. The service resolves the effective options —
// notably the roster read from the BASE ref — so the reported gate depth is the
// one actually enforced rather than the one requested.
func (f facade) VerifyRange(base, head string, opts mcpserver.RangeVerifyRequest) (mcpserver.RangeVerifyOutput, error) {
	res, err := f.svc.VerifyRange(base, head, service.RangeVerifyOptions{
		RequireSigned: opts.RequireSigned,
		TrustedKeys:   opts.TrustedKeys,
		UseRoster:     opts.UseRoster,
		SkipMerges:    opts.SkipMerges,
	})
	if err != nil {
		return mcpserver.RangeVerifyOutput{}, err
	}
	out := mcpserver.RangeVerifyOutput{
		Base:           res.Base,
		Head:           res.Head,
		OK:             res.OK(),
		Commits:        res.Commits,
		RequireSigned:  res.Effective.RequireSigned,
		TrustedKeys:    res.Effective.TrustedKeys,
		RosterFromBase: res.RosterFromBase,
	}
	for _, c := range res.Commits {
		if !c.OK() {
			out.Failed++
		}
	}
	return out, nil
}

func (f facade) Doctor(branch string) (domain.AuditReport, error) { return f.svc.Doctor(branch) }
func (f facade) Audit(branch string) (domain.AuditReport, error)  { return f.svc.Audit(branch) }

// Status assembles the gate's installed state. Every part is best-effort except
// the step list: a missing adoption point or an unreadable hook pin is a fact
// worth reporting as empty, not an error that denies the caller the rest.
func (f facade) Status() (mcpserver.StatusOutput, error) {
	pre, push, err := f.svc.StepsList()
	if err != nil {
		return mcpserver.StatusOutput{}, err
	}
	out := mcpserver.StatusOutput{
		Version:        Version,
		PreCommit:      pre,
		PrePush:        push,
		InstalledHooks: map[string]bool{},
	}
	if repo := f.svc.Repo(); repo != nil {
		out.RepoDir = repo.Dir
		if adoption, err := repo.ReadAdoption(); err == nil {
			out.Adoption = adoption
		}
	}
	if installed, err := f.svc.InstalledHooks(); err == nil {
		for hook, on := range installed {
			out.InstalledHooks[string(hook)] = on
		}
	}
	_, out.SigningKey = f.svc.SigningKey()
	return out, nil
}

var _ mcpserver.Facade = facade{}

// newFacade builds an auto-approving facade for the non-interactive agent
// surfaces (axi, MCP).
func newFacade() (facade, error) {
	svc, err := service.New(mustCwd(), Version, autoApprover{})
	if err != nil {
		return facade{}, err
	}
	return facade{svc: svc}, nil
}
