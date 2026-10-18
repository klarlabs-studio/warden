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
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// AgentStep runs a coding-agent (claude, codex, …) to perform a reasoning step
// (intent, review, document) over the worktree. It runs the shell command the
// repo configured for the resolved agent (agent_commands.<name>), expanding
// {prompt}/{step}/{repo} — Warden never guesses an agent CLI contract. The
// subprocess is flaky by nature (network, rate limits), so it runs behind a
// fortify retry + circuit-breaker chain (§4.4 resilience). When no agent is
// resolved, or no command is configured for it, the step passes marked
// StepSkipped — a repo can run the deterministic steps (lint/test) without any
// agent, and no surface will claim the agent step ran — unless a compliance
// profile requires the step to execute, which turns the skip into a failure
// with a config blocker (see StepContext.AgentBoundary).
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
		reason := "no agent configured"
		if sc.Agent != "" {
			reason = "no command configured for agent " + sc.Agent
		}
		if sc.AgentBoundary.RequireExecution {
			return s.requiredButUnconfigured(sc, reason), nil
		}
		// Still a pass — an unconfigured agent step must not fail a repo that
		// runs only deterministic checks — but marked as SKIPPED, so no surface
		// reports it as a review (or intent check, or doc pass) that happened.
		return domain.StepResult{
			Step:          s.name,
			Status:        domain.StepPass,
			Outcome:       domain.StepSkipped,
			OutcomeReason: reason,
			Summary:       string(s.name) + ": skipped (" + reason + ")",
		}, nil
	}
	if sc.AgentBoundary.EnvAllowlist && !domain.IsBundledAgent(sc.Agent) {
		// The load-time floor refuses this already; the step refuses it too, so
		// a policy built some other way cannot hand an unknown agent the
		// allowlisted environment's guarantee.
		return s.configBlocked("agent "+sc.Agent+" is not a bundled preset",
			"profile "+sc.AgentBoundary.Profile+" runs only bundled agents ("+strings.Join(domain.BundledAgents(), ", ")+
				") because a custom agent's environment needs are unknown"), nil
	}

	out, err := s.invoke(ctx, command, sc)
	var boundary *domain.AgentBoundaryObservation
	if sc.AgentBoundary.Active() {
		// The runner fills in isolation and the tree guard; the environment is
		// the step's own observation.
		boundary = &domain.AgentBoundaryObservation{EnvAllowlisted: sc.AgentBoundary.EnvAllowlist}
	}
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
			Summary:       string(s.name) + " failed",
			AgentBoundary: boundary,
		}, nil
	}
	return domain.StepResult{
		Step:          s.name,
		Status:        domain.StepPass,
		Summary:       string(s.name) + " (" + sc.Agent + ") passed: " + firstLine(out),
		AgentBoundary: boundary,
	}, nil
}

// requiredButUnconfigured is the profile's answer to an agent step with nothing
// to run it: a failure that names the profile and the config that would fix it,
// instead of the advisory skip that let a note list a review nobody performed.
func (s AgentStep) requiredButUnconfigured(sc application.StepContext, reason string) domain.StepResult {
	fix := "set `agent: " + strings.Join(domain.BundledAgents(), "` or `agent: ") + "` in .warden.yaml, or assign one with rules[].then.agent." + string(s.name)
	if sc.Agent != "" {
		fix = "add agent_commands." + sc.Agent + " to .warden.yaml, or name a bundled agent (" + strings.Join(domain.BundledAgents(), ", ") + ")"
	}
	return s.configBlocked(reason, "profile "+sc.AgentBoundary.Profile+" requires "+string(s.name)+" to run; "+fix)
}

// configBlocked fails the step without running it, for a config that cannot
// meet the profile. The config blocker makes the run exit 78, not 1: nothing
// about the change was judged.
func (s AgentStep) configBlocked(reason, message string) domain.StepResult {
	return domain.StepResult{
		Step:          s.name,
		Status:        domain.StepFail,
		Blocker:       domain.BlockerConfig,
		Outcome:       domain.StepSkipped,
		OutcomeReason: reason,
		Findings:      []domain.Finding{{Severity: domain.SeverityMedium, Message: message}},
		Summary:       string(s.name) + ": not run (" + reason + "): " + message,
	}
}

// invoke runs the agent command through a resilience chain: bounded retries with
// exponential backoff, tripping a circuit breaker on repeated failure so a
// wedged agent fails fast instead of stalling every step.
func (s AgentStep) invoke(ctx context.Context, command string, sc application.StepContext) (string, error) {
	cb := circuitbreaker.New[string](circuitbreaker.Config{
		MaxRequests: 1,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(c circuitbreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
	})
	defer func() { _ = cb.Close() }()
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
		cmd.Dir = sc.WorktreeDir
		// An agent CLI reads the repo through git like any other tool, so it
		// needs the same scrubbed environment the shell steps get. Under a
		// profile that allowlists it (strict@1), it gets only the base set and
		// its preset's documented credential variables: no cloud credentials,
		// forge tokens or other tools' keys from the developer's shell.
		cmd.Env = scrubbedEnv()
		if sc.AgentBoundary.EnvAllowlist {
			cmd.Env = domain.AgentEnv(sc.Agent, cmd.Env)
		}
		// Own process group: a timed-out or cancelled agent CLI is killed with
		// its whole subtree, not left running detached past the step.
		proc.Isolate(cmd)
		out, err := runCaptured(cmd, sc)
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
