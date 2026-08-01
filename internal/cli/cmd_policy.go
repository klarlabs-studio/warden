package cli

import (
	"flag"
	"fmt"
	"io"

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
}
