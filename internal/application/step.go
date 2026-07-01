// Package application holds Warden's use-case orchestration: the pipeline
// Runner that drives a resolved policy through the axi-go kernel, and the Step
// abstraction that both native and subprocess steps satisfy. It depends on
// domain and on infrastructure ports, never the reverse.
package application

import (
	"context"

	"go.klarlabs.de/warden/internal/domain"
)

// StepContext is everything a step needs to do its work for one run. It is
// built once per run from the resolved policy and worktree, then handed to each
// step. Steps operate against WorktreeDir — never the user's live checkout.
type StepContext struct {
	Hook        domain.Hook
	WorktreeDir string
	Branch      string
	Diff        domain.DiffStats
	// Agent is the resolved coding-agent binary for this step ("" = default).
	Agent string
	// AutoFixBudget bounds how many times an auto-fixing step may retry its
	// fix within a single run (§5.4, resolved from auto_fix.<step>).
	AutoFixBudget int
	// Commands maps shell-backed steps (lint, test) to their command line.
	Commands map[string]string
	// PriorFindings carries findings from earlier steps, so a step can react
	// to what came before (mirrors the wire protocol's prior_findings).
	PriorFindings []domain.Finding
}

// Step is one unit of the pipeline. Built-in steps are native Go
// implementations; custom steps are adapted from a subprocess. A Step must be
// side-effect-confined to sc.WorktreeDir.
type Step interface {
	// Name is the step's stable identifier, matching its config/policy name.
	Name() domain.StepName
	// Run executes the step against the worktree and reports a normalized
	// result. A returned error is an operational failure (the step could not
	// run); a StepFail status is a policy failure (the step ran and rejected).
	Run(ctx context.Context, sc StepContext) (domain.StepResult, error)
}

// Registry resolves a step name to its implementation. The runner consults it
// to assemble the actions for a run; an unknown built-in name is a
// configuration error surfaced early.
type Registry map[domain.StepName]Step
