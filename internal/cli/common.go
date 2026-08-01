package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/signing"
	"go.klarlabs.de/warden/internal/infrastructure/trust"
	"go.klarlabs.de/warden/internal/service"
)

// envMCPAllowRun opts the non-interactive agent surfaces (`warden mcp serve`
// and `warden axi run-trigger`) in to executing the current repository's
// configured commands. Those surfaces auto-approve gate findings and run the
// repo-authored `commands` as shell with no human in the loop, so pointing an
// MCP client at an untrusted clone and letting it call run_trigger would be
// arbitrary code execution. run_trigger therefore refuses by default and runs
// only when the operator has explicitly trusted this repo via this env var (or
// the axi `--trust` flag). The read-only surfaces (policy_explain, steps_list)
// never consult it.
const envMCPAllowRun = "WARDEN_MCP_ALLOW_RUN"

// mcpRunTrusted reports whether the operator has explicitly opted this
// non-interactive run in, checking three grants in order of specificity:
//
//  1. explicit — a surface-local signal (the axi `--trust` flag), scoped to the
//     single invocation that carries it.
//  2. the per-repo allowlist (`warden trust add`), which names its subject.
//  3. the env var, which does not.
//
// (2) exists because (3) answers the wrong question. WARDEN_MCP_ALLOW_RUN=1 says
// "this server may run commands", not "this server may run THIS repo's
// commands" — and an MCP server is long-lived while an agent moves between
// checkouts, so a single grant silently covered every repo the server was later
// pointed at, including one cloned minutes afterwards. The env var is kept: in a
// container or CI job there is no persistent config dir to hold an allowlist,
// and the whole workspace is disposable anyway.
func mcpRunTrusted(explicit bool) bool {
	if explicit || truthyEnv(os.Getenv(envMCPAllowRun)) {
		return true
	}
	return repoTrusted()
}

// repoTrusted reports whether the CURRENT repository is on the operator's
// allowlist. It fails closed: when the config dir or the working directory
// cannot be resolved, trust has not been established, and the safe reading of
// "I don't know" is "no".
func repoTrusted() bool {
	dir, err := signing.DefaultDir()
	if err != nil {
		return false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	return trust.New(dir).Trusted(trustSubject(cwd))
}

// trustSubject is the path a grant is recorded against: the repository ROOT when
// there is one, so a call from a subdirectory resolves to the same grant instead
// of needing one per directory.
func trustSubject(cwd string) string {
	if repo, err := git.Open(cwd); err == nil && repo.Dir != "" {
		return repo.Dir
	}
	return cwd
}

// truthyEnv treats the common affirmative spellings as "on"; everything else,
// including empty and unset, is off so the gate stays refuse-by-default.
func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// errUntrustedMCPRun is the refusal returned when run_trigger is invoked on a
// repo the operator has not trusted. It names the exact opt-in so the message
// is actionable rather than a bare "denied".
func errUntrustedMCPRun() error {
	return fmt.Errorf(
		"run_trigger refused: this surface auto-approves and runs the repository's configured commands as shell with no human in the loop, so it will not execute a possibly-untrusted repo's .warden.yaml. Trust this one repository with `warden trust add` (preferred — the grant names its subject), pass --trust to `warden axi run-trigger` for a single invocation, or set %s=1 to trust EVERY repo this process is pointed at",
		envMCPAllowRun,
	)
}

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
