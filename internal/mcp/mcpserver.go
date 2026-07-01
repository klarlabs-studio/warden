// Package mcpserver exposes Warden's operation set as MCP tools (spec §4.6) so
// an AI agent can drive the same gate a human runs from the CLI.
//
// It depends only on the narrow Facade interface defined here, never on the
// concrete service: the CLI wires the real implementation into NewServer. That
// inversion keeps the MCP surface a thin, delivery-neutral adapter and lets the
// tool handlers be unit-tested against a fake without spinning up a pipeline.
package mcpserver

import (
	"context"
	"fmt"

	"go.klarlabs.de/mcp"

	"go.klarlabs.de/warden/internal/domain"
)

// Facade is the subset of Warden operations the MCP surface needs. The CLI
// wires a concrete implementation (the service) into NewServer.
type Facade interface {
	// PolicyExplain resolves effective policy for a hypothetical invocation.
	PolicyExplain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error)
	// StepsList returns built-in + configured step names grouped by hook.
	StepsList() (preCommit, prePush []domain.StepName, err error)
	// RunTrigger runs the pipeline for a hook and returns a compact summary.
	RunTrigger(ctx context.Context, hook domain.Hook) (RunSummary, error)
}

// RunSummary is a delivery-neutral run result the MCP tool returns.
type RunSummary struct {
	Outcome  string            `json:"outcome"` // passed|failed|rejected|aborted
	Hook     string            `json:"hook"`
	Steps    []domain.StepName `json:"steps"`
	Findings []domain.Finding  `json:"findings"`
	Message  string            `json:"message"`
	RunID    string            `json:"run_id,omitempty"`
}

// PolicyExplainInput is the argument schema for the policy_explain tool. Branch
// and paths are optional so an agent can probe policy for a bare hook.
type PolicyExplainInput struct {
	Hook   string   `json:"hook" jsonschema:"required,description=Hook to resolve policy for: pre-commit or pre-push"`
	Branch string   `json:"branch,omitempty" jsonschema:"description=Branch name the invocation targets (optional)"`
	Paths  []string `json:"paths,omitempty" jsonschema:"description=Repo-relative paths touched, for path-glob rule matching (optional)"`
}

// StepsListInput takes no arguments; steps_list is a pure read of configuration.
type StepsListInput struct{}

// StepsListOutput groups step names by hook, matching the two hook points.
type StepsListOutput struct {
	PreCommit []domain.StepName `json:"pre_commit"`
	PrePush   []domain.StepName `json:"pre_push"`
}

// RunTriggerInput is the argument schema for the run_trigger tool.
type RunTriggerInput struct {
	Hook string `json:"hook" jsonschema:"required,description=Hook pipeline to run: pre-commit or pre-push"`
}

// NewServer builds an MCP server exposing Warden's operation set as typed tools:
//   - policy_explain(hook, branch?, paths?) -> ResolvedPolicy
//   - steps_list() -> {pre_commit, pre_push}
//   - run_trigger(hook) -> RunSummary
//
// run_respond/run_status are intentionally absent: v0 runs synchronously, so
// there is no out-of-band run to poll or respond to. A stub tool documents this
// rather than silently omitting the operation, so an agent gets a clear error.
func NewServer(f Facade, version string) *mcp.Server {
	srv := mcp.NewServer(mcp.ServerInfo{
		Name:        "warden",
		Version:     version,
		Title:       "Warden",
		Description: "Git commit/push gate: explain policy, list steps, and run the pipeline.",
	})

	srv.Tool("policy_explain").
		Description("Resolve the effective policy for a hypothetical hook invocation.").
		ReadOnly().
		Handler(func(in PolicyExplainInput) (domain.ResolvedPolicy, error) {
			return handlePolicyExplain(f, in)
		})

	srv.Tool("steps_list").
		Description("List built-in and configured step names grouped by hook.").
		ReadOnly().
		Handler(func(StepsListInput) (StepsListOutput, error) {
			return handleStepsList(f)
		})

	srv.Tool("run_trigger").
		Description("Run the pipeline for a hook and return a compact run summary.").
		Handler(func(ctx context.Context, in RunTriggerInput) (RunSummary, error) {
			return handleRunTrigger(ctx, f, in)
		})

	srv.Tool("run_respond").
		Description("Not supported in synchronous v0: runs complete inline, so there is no pending run to respond to.").
		Handler(func(map[string]any) (struct{}, error) {
			return struct{}{}, errNotSupported("run_respond")
		})

	srv.Tool("run_status").
		Description("Not supported in synchronous v0: run_trigger returns the final outcome directly.").
		Handler(func(map[string]any) (struct{}, error) {
			return struct{}{}, errNotSupported("run_status")
		})

	return srv
}

// Serve starts the server on stdio and blocks until ctx is canceled.
func Serve(ctx context.Context, f Facade, version string) error {
	return mcp.ServeStdio(ctx, NewServer(f, version))
}

// handlePolicyExplain parses the hook and delegates to the facade. It is split
// out of the tool closure so it can be unit-tested directly against a fake.
func handlePolicyExplain(f Facade, in PolicyExplainInput) (domain.ResolvedPolicy, error) {
	hook, err := domain.ParseHook(in.Hook)
	if err != nil {
		return domain.ResolvedPolicy{}, err
	}
	return f.PolicyExplain(hook, in.Branch, in.Paths)
}

// handleStepsList maps the facade's two-return-value shape onto the typed output
// struct so the tool marshals a stable, self-describing JSON object.
func handleStepsList(f Facade) (StepsListOutput, error) {
	preCommit, prePush, err := f.StepsList()
	if err != nil {
		return StepsListOutput{}, err
	}
	return StepsListOutput{PreCommit: preCommit, PrePush: prePush}, nil
}

// handleRunTrigger parses the hook and runs the pipeline, propagating context so
// the run honors cancellation from the MCP client.
func handleRunTrigger(ctx context.Context, f Facade, in RunTriggerInput) (RunSummary, error) {
	hook, err := domain.ParseHook(in.Hook)
	if err != nil {
		return RunSummary{}, err
	}
	return f.RunTrigger(ctx, hook)
}

// errNotSupported reports an operation that has no meaning in synchronous v0.
func errNotSupported(op string) error {
	return fmt.Errorf("%s is not supported: Warden v0 runs synchronously, so run_trigger returns the final outcome directly", op)
}
