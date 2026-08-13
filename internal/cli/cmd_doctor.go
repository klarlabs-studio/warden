package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdDoctor handles `warden doctor`, auditing which commits since adoption carry
// a validation note and whether each note's evidence chain is intact (§9).
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branchFlag := fs.String("branch", "", "branch to audit (default: current)")
	ci := fs.Bool("ci", false, "exit "+fmt.Sprint(exitDoctorDrift)+" on drift, so CI can tell drift from a doctor that could not run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "doctor", "branch") {
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

	// Gate on Unverified alone. It excludes commits a gated push span published,
	// and commits on a branch the pre-push gate never had a chance to run on —
	// both of which printDoctor already reports as fine, one of them with "These
	// are not bypasses" written directly above this exit code.
	if report.Counts().Unverified > 0 {
		// Signal drift so CI can gate on it, without treating it as a crash.
		if *ci {
			return exitDoctorDrift
		}
		return 1
	}
	return 0
}

// exitDoctorDrift is `warden doctor --ci`'s exit code for "the branch has
// unverified commits".
//
// Without it, drift and a doctor that could not run at all are both 1 — an
// unadopted repo, a shallow clone with no history to walk, a broken notes fetch.
// A CI job cannot tell them apart, so the failure mode is a check that reports
// tidy, actionable drift when warden in fact never audited anything. That is the
// same shape as #212 §2's original complaint (a check that looks like coverage),
// which is not a bug worth reintroducing while fixing it.
//
// It lives behind --ci rather than replacing the default, because gating on
// `warden doctor` exiting 1 is an existing contract.
const exitDoctorDrift = 3

func printDoctor(w io.Writer, r domain.AuditReport) {
	_, _ = fmt.Fprintf(w, "branch %s since adoption %s:\n", r.Branch, short(r.Adoption))
	for i := range r.Commits {
		c := &r.Commits[i]
		switch {
		case c.HasNote:
			// Not a blanket "TAMPERED": that word was printed for three different
			// failures, and the commonest — a note left behind by a rebase — is not
			// tampering at all. Only a broken chain earns it.
			//
			// The glyph must not contradict the label either: "✓ … TAMPERED" reads
			// as a pass at a glance, and a glance is all most of these lines get.
			glyph, state := "✓", "chain-intact"
			if !c.ChainIntact {
				glyph, state = "⚠", noteDefectLabel(c.NoteDefect)
			}
			_, _ = fmt.Fprintf(w, "  %s %s  %s  %s  (%s, %d steps, %s)\n",
				glyph, short(c.SHA), c.Date, truncate(c.Subject, 40), c.RunID, len(c.Steps), state)
		case c.Covered():
			// Published by a gated push, just not individually attested — warden
			// validates one tree per run and vouches for the span. Reporting this
			// as UNVERIFIED read as "never checked" and was simply wrong.
			_, _ = fmt.Fprintf(w, "  ✓ %s  %s  %s  (covered by the gated push %s)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40), short(c.CoveredBy))
		case c.Unpushed():
			// The pre-push gate is what writes the note, and this branch never
			// reached it. Reporting these as bypasses accused someone of evading
			// a gate that was never reachable.
			_, _ = fmt.Fprintf(w, "  ? %s  %s  %s  UNPUSHED (never pushed; the pre-push gate has not run)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		case c.Unattributable():
			_, _ = fmt.Fprintf(w, "  ? %s  %s  %s  UNATTRIBUTABLE (provenance predates push spans)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		case c.Reattestable():
			// The content WAS gated — under the pre-squash commit id. Say which
			// one, so the reader sees a recoverable binding gap rather than an
			// unchecked commit.
			_, _ = fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (reattestable from %s)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40), short(c.ReattestableFrom))
		default:
			_, _ = fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (no warden note)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		}
	}
	t := r.Counts()
	_, _ = fmt.Fprint(w, summaryLine("", t))
	if r.NeverPushed {
		_, _ = fmt.Fprintf(w, "branch %s has no remote-tracking ref: nothing here has been pushed, so the\n"+
			"pre-push gate that writes the note has never run. These are not bypasses.\n", r.Branch)
	}
	if n := len(r.Reattestable()); n > 0 {
		_, _ = fmt.Fprintf(w, "%d of the %d were gated under a different commit id (squash-merge); recover them with:\n"+
			"  warden reattest --all --branch %s --push\n", n, t.Unverified, r.Branch)
	}
}

// summaryLine renders the counts line shared by doctor and audit.
//
// "verified" now means the note ATTESTS the commit, not merely that one exists.
// A defective note is called out separately rather than folded into verified
// with a parenthetical: `warden verify` refuses those commits outright, and a
// summary that calls them verified is the tool contradicting itself on the one
// line most readers stop at.
// Covered and unknown commits are named rather than folded into either side:
// summing them into unverified made a fully-gated multi-commit push read as a
// pile of unchecked commits, and dropping them silently would leave a summary
// whose numbers no longer add up to the history it just listed.
func summaryLine(prefix string, t domain.Tally) string {
	s := fmt.Sprintf("%s%d verified, %d unverified since adoption", prefix, t.Verified, t.Unverified)
	if t.Defective > 0 {
		s += fmt.Sprintf(" (%d of them carry a note that does not attest the commit)", t.Defective)
	}
	if t.Covered > 0 {
		s += fmt.Sprintf("; %d covered by a gated push span", t.Covered)
	}
	if t.Unknown > 0 {
		s += fmt.Sprintf("; %d unattributable (never pushed, or provenance predating push spans)", t.Unknown)
	}
	return s + "\n"
}

// noteDefectLabel renders why a note failed to attest its commit.
//
// "TAMPERED" is reserved for the one defect that actually suggests it. A note
// that is internally sound but describes a different commit is what a rebase or
// squash leaves behind, and accusing someone of tampering because they rewrote
// their own history is the same unevidenced claim as calling an unpushed commit
// a bypass. The verdict is unchanged — none of these count as verified.
func noteDefectLabel(defect string) string {
	switch defect {
	case domain.DefectChainBroken:
		return "TAMPERED (evidence chain broken)"
	case domain.DefectUnbound:
		return "UNBOUND (note describes another commit — history was rewritten)"
	case domain.DefectNoEvidence:
		return "NO EVIDENCE (note records no steps)"
	default:
		// An unrecognized defect must still read as "do not trust this", never as
		// a blank that looks like a pass.
		return "UNVERIFIED"
	}
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
