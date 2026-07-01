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

// AgentStep runs a coding-agent (claude, codex, …) to perform a reasoning step
// (intent, review, document) over the worktree. It runs the shell command the
// repo configured for the resolved agent (agent_commands.<name>), expanding
// {prompt}/{step}/{repo} — Warden never guesses an agent CLI contract. The
// subprocess is flaky by nature (network, rate limits), so it runs behind a
// fortify retry + circuit-breaker chain (§4.4 resilience). When no agent is
// resolved, or no command is configured for it, the step is an advisory pass —
// a repo can run the deterministic steps (lint/test) without any agent.
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
	command := expandTemplate(sc.AgentCommand, s.prompt, s.name, sc.WorktreeDir)
	if sc.Agent == "" || strings.TrimSpace(command) == "" {
		return domain.StepResult{
			Step:    s.name,
			Status:  domain.StepPass,
			Summary: string(s.name) + ": no agent command configured, skipped",
		}, nil
	}

	out, err := s.invoke(ctx, command, sc.WorktreeDir)
	if err != nil {
		// A genuinely failed agent run (after retries) is reported as a finding
		// rather than an operational error, so the pipeline decides the outcome.
		return domain.StepResult{
			Step:   s.name,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityMedium,
				Message:  string(s.name) + " agent (" + sc.Agent + ") failed: " + strings.TrimSpace(err.Error()),
			}},
			Summary: string(s.name) + " failed",
		}, nil
	}
	return domain.StepResult{
		Step:    s.name,
		Status:  domain.StepPass,
		Summary: string(s.name) + " (" + sc.Agent + ") passed: " + firstLine(out),
	}, nil
}

// invoke runs the agent command through a resilience chain: bounded retries with
// exponential backoff, tripping a circuit breaker on repeated failure so a
// wedged agent fails fast instead of stalling every step.
func (s AgentStep) invoke(ctx context.Context, command, workdir string) (string, error) {
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
		// Run through the shell so the configured command may use the agent's
		// own flags and pipes; a zero exit means the step passed.
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = workdir
		out, err := cmd.CombinedOutput()
		return string(out), err
	})
}

// expandTemplate substitutes {prompt}, {step}, and {repo} placeholders in an
// agent command template. The prompt is single-quote-escaped so an instruction
// containing shell metacharacters can't break out of the command.
func expandTemplate(tmpl, prompt string, step domain.StepName, repo string) string {
	if tmpl == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"{prompt}", shellQuote(prompt),
		"{step}", string(step),
		"{repo}", shellQuote(repo),
	)
	return replacer.Replace(tmpl)
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes, so
// it is safe to interpolate into an `sh -c` command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
