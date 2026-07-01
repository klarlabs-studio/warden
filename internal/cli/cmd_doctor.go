package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/service"
)

// cmdDoctor handles `warden doctor`, auditing which commits since adoption carry
// a validation note and whether each note's evidence chain is intact (§9).
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branchFlag := fs.String("branch", "", "branch to audit (default: current)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	report, err := svc.Doctor(*branchFlag)
	if err != nil {
		return fail(stderr, err)
	}
	printDoctor(stdout, report)

	_, _, unverified := report.Counts()
	if unverified > 0 {
		// Signal drift so CI can gate on it, without treating it as a crash.
		return 1
	}
	return 0
}

func printDoctor(w io.Writer, r service.DoctorReport) {
	fmt.Fprintf(w, "branch %s since adoption %s:\n", r.Branch, short(r.Adoption))
	for _, c := range r.Commits {
		if c.HasNote {
			state := "chain-intact"
			if !c.ChainIntact {
				state = "TAMPERED"
			}
			fmt.Fprintf(w, "  ✓ %s  %s  %s  (%s, %d steps, %s)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40), c.RunID, len(c.Steps), state)
		} else {
			fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (no warden note)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		}
	}
	verified, intact, unverified := r.Counts()
	fmt.Fprintf(w, "%d verified (%d chain-intact), %d unverified since adoption\n", verified, intact, unverified)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
