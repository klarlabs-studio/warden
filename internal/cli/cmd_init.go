package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdInit handles `warden init [--hooks=...]`, installing the selected hooks,
// writing a starter config, and recording the adoption point.
func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hooksFlag := fs.String("hooks", "", "comma-separated hooks to install (default: pre-commit,pre-push)")
	reAdopt := fs.Bool("re-adopt", false, "move the adoption point to HEAD on an already-adopted repository (narrows the audit range)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "init", "") {
		return 2
	}

	selected, err := parseHooksFlag(*hooksFlag)
	if err != nil {
		return fail(stderr, err)
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.Init(selected, *reAdopt)
	if err != nil {
		return fail(stderr, err)
	}

	verb := "initialized"
	if res.Adopted() {
		verb = "re-armed"
	}
	_, _ = fmt.Fprintf(stdout, "warden %s. installed hooks:", verb)
	for _, h := range selected {
		_, _ = fmt.Fprintf(stdout, " %s", h)
	}
	_, _ = fmt.Fprintln(stdout)
	if res.Lang != domain.LangUnknown {
		_, _ = fmt.Fprintf(stdout, "detected %s — pre-filled lint/test commands in .warden.yaml (adjust as needed).\n", res.Lang)
	}
	writeAdoptionOutcome(stdout, res)
	return 0
}

// writeAdoptionOutcome says what happened to the adoption point, in every
// case including the one where nothing happened.
//
// The old message was the same sentence either way — "adoption point
// recorded at current HEAD" — on a first init and on a second one that
// had just moved the point forward and dropped every commit in between
// out of `warden doctor`'s range. A provenance tool narrowing its own
// audit is the one thing it must never do quietly.
func writeAdoptionOutcome(stdout io.Writer, res service.InitResult) {
	switch {
	case !res.Adopted():
		_, _ = fmt.Fprintf(stdout, "adoption point recorded at %s; edit .warden.yaml to configure policy.\n",
			shortSHA(res.Adoption))
	case !res.Moved:
		_, _ = fmt.Fprintf(stdout,
			"adoption point kept at %s — this repository had already adopted warden.\n"+
				"  pass --re-adopt to move it to HEAD; doing so drops the commits in between out of `warden doctor`.\n",
			shortSHA(res.Adoption))
	default:
		_, _ = fmt.Fprintf(stdout, "adoption point MOVED %s -> %s.\n",
			shortSHA(res.Previous), shortSHA(res.Adoption))
		if res.LeftAudit > 0 {
			_, _ = fmt.Fprintf(stdout,
				"  %d commit(s) are no longer in `warden doctor`'s audit range.\n", res.LeftAudit)
		}
	}
}

// shortSHA abbreviates for display without pretending a missing value is
// a real one. `short` alone renders "" as "", which reads as a SHA that
// failed to print rather than as one that was never recorded.
func shortSHA(sha string) string {
	if sha == "" {
		return "(none)"
	}
	return short(sha)
}
