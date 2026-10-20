package cli

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/explain"
)

// cmdPolicy handles `warden policy explain`, printing the resolved effective
// config for a hypothetical invocation — the intended mitigation for a
// misconfigured rule silently gutting the gate (§5.2).
func cmdPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "explain" {
		_, _ = fmt.Fprintln(stderr, "usage: warden policy explain [--hook h] [--branch b] [--paths glob,...] [--chart]")
		return 2
	}

	fs := flag.NewFlagSet("policy explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hookFlag := fs.String("hook", "pre-push", "hook to explain")
	branchFlag := fs.String("branch", "", "branch to evaluate (default: current)")
	pathsFlag := fs.String("paths", "", "comma-separated changed paths to simulate")
	chartFlag := fs.Bool("chart", false, "emit an XState-compatible statechart JSON instead")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "policy explain", "") {
		return 2
	}

	hook, err := domain.ParseHook(*hookFlag)
	if err != nil {
		return fail(stderr, err)
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	resolved, err := svc.Explain(hook, *branchFlag, splitList(*pathsFlag))
	if err != nil {
		return fail(stderr, err)
	}

	if *chartFlag {
		chart, err := explain.Chart(resolved)
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintln(stdout, chart)
		return 0
	}

	printPolicy(stdout, resolved)
	return 0
}

func printPolicy(w io.Writer, p domain.ResolvedPolicy) {
	_, _ = fmt.Fprintf(w, "hook:            %s\n", p.Hook)
	_, _ = fmt.Fprintf(w, "risk:            %s\n", p.Risk)
	_, _ = fmt.Fprintf(w, "require_approval: %v\n", p.RequireApproval)
	_, _ = fmt.Fprintf(w, "steps:          ")
	for _, s := range p.Steps {
		_, _ = fmt.Fprintf(w, " %s", s)
		if a := p.AgentFor(s); a != "" {
			_, _ = fmt.Fprintf(w, "(agent=%s)", a)
		}
		if b := p.AutoFixBudget(s); b > 0 {
			_, _ = fmt.Fprintf(w, "(auto_fix=%d)", b)
		}
	}
	_, _ = fmt.Fprintln(w)

	// Schedule: show which steps run concurrently vs. as sequential barriers, so
	// the effect of `parallel` and auto-fix budgets is visible.
	_, _ = fmt.Fprintf(w, "schedule:       ")
	for i, batch := range p.Batches() {
		if i > 0 {
			_, _ = fmt.Fprintf(w, " → ")
		}
		if len(batch) == 1 {
			_, _ = fmt.Fprintf(w, "%s", batch[0])
			continue
		}
		_, _ = fmt.Fprintf(w, "[")
		for j, s := range batch {
			if j > 0 {
				_, _ = fmt.Fprintf(w, " ∥ ")
			}
			_, _ = fmt.Fprintf(w, "%s", s)
		}
		_, _ = fmt.Fprintf(w, "]")
	}
	_, _ = fmt.Fprintln(w)

	if len(p.MatchedRules) > 0 {
		_, _ = fmt.Fprintf(w, "matched rules:  ")
		for _, r := range p.MatchedRules {
			_, _ = fmt.Fprintf(w, " [%s]", r)
		}
		_, _ = fmt.Fprintln(w)
	} else {
		_, _ = fmt.Fprintln(w, "matched rules:   (none)")
	}
	printProfile(w, p.Profile)
	printWorkspaces(w, p)
}

// printWorkspaces shows the resolved monorepo configuration, and that this
// release does not act on it. It prints nothing when the config sets neither
// workspaces: nor per_workspace:, so every other repo's explain output is
// unchanged.
func printWorkspaces(w io.Writer, p domain.ResolvedPolicy) {
	ws := p.Workspaces
	if ws == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "workspaces:      discover=%s select=%s unowned=%s concurrency=%s\n",
		orNone(strings.Join(ws.Discover, ",")), ws.Select, ws.Rules.Unowned, concurrencyLabel(ws.Concurrency))
	_, _ = fmt.Fprintf(w, "  global:        %s (always: %s)\n", orNone(strings.Join(ws.Rules.Global, " ")), strings.Join(domain.DefaultGlobalPaths, " "))
	_, _ = fmt.Fprintf(w, "  ignore:        %s\n", orNone(strings.Join(ws.Rules.Ignore, " ")))
	if len(ws.Steps) > 0 {
		_, _ = fmt.Fprintln(w, "per_workspace:")
		names := make([]domain.StepName, 0, len(ws.Steps))
		for s := range ws.Steps {
			names = append(names, s)
		}
		slices.Sort(names)
		for _, s := range names {
			def := ws.Steps[s]
			line := fmt.Sprintf("  %s: %s %q", s, def.Mode(), def.Command())
			if len(def.Ecosystems) > 0 {
				line += " ecosystems=" + strings.Join(def.Ecosystems, ",")
			}
			if slices.Contains(p.Steps, s) {
				line += " (in this hook's steps: it does not run per workspace yet)"
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
	if !ws.Active {
		_, _ = fmt.Fprintf(w, "NOTE: %s.\n", ws.Notice)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func concurrencyLabel(n int) string {
	if n == 0 {
		return "default"
	}
	return strconv.Itoa(n)
}

// printProfile shows the compliance profile in force and each floor's status.
// It prints nothing without a profile, so an unprofiled repo's explain output
// is unchanged.
func printProfile(w io.Writer, prof *domain.ResolvedProfile) {
	if prof == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "profile:         %s (%s)\n", prof.Name, prof.Digest)
	_, _ = fmt.Fprintln(w, "floors:")
	for _, f := range prof.Floors {
		mark := "ok  "
		if !f.Satisfied {
			mark = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "  %s %s: %s\n", mark, f.ID, f.Description)
		for _, problem := range f.Problems {
			_, _ = fmt.Fprintf(w, "       %s\n", problem)
		}
	}
}
