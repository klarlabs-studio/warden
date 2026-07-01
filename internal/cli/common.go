package cli

import (
	"context"
	"os"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// newService opens the repository at the current working directory and wires
// the service with the given approver.
func newService(approver application.Approver) (*service.Service, error) {
	return service.New(mustCwd(), Version, approver)
}

// mustCwd returns the working directory, or "." if it cannot be determined.
func mustCwd() string {
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// autoApprover approves every gate; used by non-interactive surfaces that have
// no human to consult and choose to let clean-but-flagged runs proceed.
type autoApprover struct{}

func (autoApprover) Approve(_ context.Context, _ application.ApprovalRequest) (application.Decision, error) {
	return application.Decision{Approved: true, Principal: "warden-auto"}, nil
}

// splitList parses a comma-separated flag value into trimmed, non-empty parts.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseHooksFlag turns "pre-commit,pre-push" into hooks, defaulting to both.
func parseHooksFlag(v string) ([]domain.Hook, error) {
	if strings.TrimSpace(v) == "" {
		return domain.AllHooks, nil
	}
	var hooks []domain.Hook
	for _, part := range splitList(v) {
		h, err := domain.ParseHook(part)
		if err != nil {
			return nil, err
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}
