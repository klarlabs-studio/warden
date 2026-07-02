// Package steps holds Warden's built-in step implementations. Each satisfies
// application.Step and confines its side effects to the run's worktree.
package steps

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// ShellStep runs a configured shell command (lint, test) in the worktree. A
// zero exit is a pass; any non-zero exit fails the step with the command's
// combined output captured as a finding, so the developer sees exactly why.
type ShellStep struct {
	name domain.StepName
	// cmdKey is the key into StepContext.Commands (e.g. "lint", "test").
	cmdKey string
}

// NewShellStep binds a step name to the command key it runs.
func NewShellStep(name domain.StepName, cmdKey string) ShellStep {
	return ShellStep{name: name, cmdKey: cmdKey}
}

func (s ShellStep) Name() domain.StepName { return s.name }

func (s ShellStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	command := sc.Commands[s.cmdKey]
	if strings.TrimSpace(command) == "" {
		// No command configured: the step is a no-op pass rather than a hard
		// failure, so a repo can adopt Warden before wiring every command.
		return domain.StepResult{
			Step:    s.name,
			Status:  domain.StepPass,
			Summary: string(s.name) + ": no command configured, skipped",
		}, nil
	}

	// Run through the shell so configured commands may use pipes and globs,
	// matching how a developer would run them.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = sc.WorktreeDir
	cmd.Env = stepEnv(sc)
	out, err := runCaptured(cmd, sc)
	if err != nil {
		return domain.StepResult{
			Step:   s.name,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityHigh,
				Message:  strings.TrimSpace(string(out)),
			}},
			Summary: string(s.name) + " failed",
		}, nil
	}
	return domain.StepResult{
		Step:    s.name,
		Status:  domain.StepPass,
		Summary: string(s.name) + " passed",
	}, nil
}

// stepEnv augments the process environment with WARDEN_* variables so a command
// can scope itself to what changed — the primitive for incremental checks. For
// example: `go test $(echo "$WARDEN_CHANGED_FILES" | ...)`. The full change set
// (not just a per-step subset) is exposed; scoping is the command's choice.
func stepEnv(sc application.StepContext) []string {
	env := os.Environ()
	env = append(env,
		"WARDEN_HOOK="+sc.Hook.ConfigKey(),
		"WARDEN_BRANCH="+sc.Branch,
		"WARDEN_CHANGED_FILES="+strings.Join(sc.Diff.Paths, "\n"),
		"WARDEN_FILES_TOUCHED="+strconv.Itoa(sc.Diff.FilesTouched),
		"WARDEN_LINES_CHANGED="+strconv.Itoa(sc.Diff.LinesChanged),
	)
	return env
}
