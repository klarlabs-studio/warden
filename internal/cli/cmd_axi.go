package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/axi/toon"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdAxi is the flags-only, non-interactive agent surface (§4.6). It emits TOON
// (Token-Optimized Object Notation) on stdout rather than JSON, so an agent
// consuming it spends ~40% fewer tokens on the uniform result shapes.
func cmdAxi(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: warden axi <policy-explain|steps|run-trigger> [flags]")
		return 2
	}
	verb, rest := args[0], args[1:]

	f, err := newFacade()
	if err != nil {
		return fail(stderr, err)
	}

	switch verb {
	case "policy-explain":
		return axiPolicyExplain(f, rest, stdout, stderr)
	case "steps":
		return axiSteps(f, stdout, stderr)
	case "run-trigger":
		return axiRunTrigger(f, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "warden axi: unknown verb %q\n", verb)
		return 2
	}
}

func axiPolicyExplain(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi policy-explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookFlag := fs.String("hook", "pre-push", "hook")
	branchFlag := fs.String("branch", "", "branch")
	pathsFlag := fs.String("paths", "", "comma-separated paths")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hook, err := domain.ParseHook(*hookFlag)
	if err != nil {
		return fail(stderr, err)
	}
	resolved, err := f.PolicyExplain(hook, *branchFlag, splitList(*pathsFlag))
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"hook":             string(resolved.Hook),
		"risk":             string(resolved.Risk),
		"require_approval": resolved.RequireApproval,
		"steps":            stepStrings(resolved.Steps),
		"matched_rules":    anyStrings(resolved.MatchedRules),
	})
}

func axiSteps(f facade, stdout, stderr io.Writer) int {
	pre, push, err := f.StepsList()
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"pre_commit": stepStrings(pre),
		"pre_push":   stepStrings(push),
	})
}

func axiRunTrigger(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi run-trigger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookFlag := fs.String("hook", "pre-push", "hook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hook, err := domain.ParseHook(*hookFlag)
	if err != nil {
		return fail(stderr, err)
	}
	summary, err := f.RunTrigger(context.Background(), hook)
	if err != nil {
		return fail(stderr, err)
	}
	return emitTOON(stdout, stderr, map[string]any{
		"outcome": summary.Outcome,
		"hook":    summary.Hook,
		"message": summary.Message,
		"run_id":  summary.RunID,
	})
}

func emitTOON(stdout, stderr io.Writer, v any) int {
	out, err := toon.Encode(v)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, out)
	return 0
}

func stepStrings(steps []domain.StepName) []any {
	out := make([]any, len(steps))
	for i, s := range steps {
		out[i] = string(s)
	}
	return out
}

// anyStrings widens a []string to []any, which is the slice shape TOON encodes.
func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
