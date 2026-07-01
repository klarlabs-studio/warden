package steps

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"go.klarlabs.de/fortify/circuitbreaker"
	"go.klarlabs.de/fortify/middleware"
	"go.klarlabs.de/fortify/retry"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// AgentStep runs a coding-agent binary (claude, codex, …) to perform a
// reasoning step (intent, review, document) over the worktree. The agent
// subprocess is flaky by nature — network, rate limits — so it runs behind a
// fortify retry + circuit-breaker chain (§4.4 resilience). When no agent is
// resolved or present on PATH the step is an advisory pass, so a repo can run
// the deterministic steps (lint/test) without an agent configured.
type AgentStep struct {
	name   domain.StepName
	prompt string
}

// NewAgentStep binds a step name to the instruction handed to the agent.
func NewAgentStep(name domain.StepName, prompt string) AgentStep {
	return AgentStep{name: name, prompt: prompt}
}

func (s AgentStep) Name() domain.StepName { return s.name }

func (s AgentStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	bin := resolveAgentBinary(sc.Agent)
	if bin == "" {
		return domain.StepResult{
			Step:    s.name,
			Status:  domain.StepPass,
			Summary: string(s.name) + ": no agent available, skipped",
		}, nil
	}

	out, err := s.invoke(ctx, bin, sc)
	if err != nil {
		// A genuinely failed agent run (after retries) is reported as a finding
		// rather than an operational error, so the pipeline decides the outcome.
		return domain.StepResult{
			Step:   s.name,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityMedium,
				Message:  string(s.name) + " agent failed: " + strings.TrimSpace(err.Error()),
			}},
			Summary: string(s.name) + " failed",
		}, nil
	}
	return domain.StepResult{
		Step:    s.name,
		Status:  domain.StepPass,
		Summary: string(s.name) + " (" + bin + ") passed: " + firstLine(out),
	}, nil
}

// invoke runs the agent through a resilience chain: bounded retries with
// exponential backoff, tripping a circuit breaker on repeated failure so a
// wedged agent fails fast instead of stalling every step.
func (s AgentStep) invoke(ctx context.Context, bin string, sc application.StepContext) (string, error) {
	cb := circuitbreaker.New[string](circuitbreaker.Config{
		MaxRequests: 1,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(c circuitbreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
	})
	defer cb.Close()
	r := retry.New[string](retry.Config{
		MaxAttempts:  3,
		InitialDelay: 500 * time.Millisecond,
		Jitter:       true,
		IsRetryable: func(err error) bool {
			return !errors.Is(err, context.Canceled)
		},
	})
	chain := middleware.New[string]().WithCircuitBreaker(cb).WithRetry(r)

	return chain.Execute(ctx, func(ctx context.Context) (string, error) {
		// Convention: the agent binary receives the step id and instruction as
		// args and the worktree as its cwd; a zero exit means the step passed.
		cmd := exec.CommandContext(ctx, bin, "warden-step", string(s.name), s.prompt)
		cmd.Dir = sc.WorktreeDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	})
}

// resolveAgentBinary maps a resolved agent selection to an executable on PATH.
// Only an explicitly configured agent name runs: "auto" (or empty) yields "" so
// the step is an advisory skip. Auto-probing PATH for a coding-agent binary and
// invoking it would be unsafe — the binary's real CLI contract is unknown, so a
// bare-name match could run something with arguments it never understood. A
// repo opts into agent steps by naming the agent (and the warden-step
// convention it honors) in policy.
func resolveAgentBinary(agent string) string {
	if agent == "" || agent == "auto" {
		return ""
	}
	if p, err := exec.LookPath(agent); err == nil {
		return p
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
