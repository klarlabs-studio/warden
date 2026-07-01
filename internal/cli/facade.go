package cli

import (
	"context"

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
	}
	if res.Record != nil {
		summary.RunID = res.Record.RunID
	}
	return summary, nil
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
