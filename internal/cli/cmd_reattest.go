package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/service"
)

// cmdReattest handles `warden reattest`: give an un-noted commit — typically a
// squash-merge commit on the base branch — a provenance note carried from the
// already-validated commit whose tree it reproduces, re-signed locally. It
// closes the squash-merge gap so `warden doctor`/`audit` on the base branch stay
// green, without a hosted bot or a CI signing key: the maintainer, whose key is
// already trusted, vouches locally. Exits non-zero when no content-identical
// validated commit exists (so nothing was written).
func cmdReattest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reattest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.String("commit", "HEAD", "commit to re-attest")
	all := fs.Bool("all", false, "re-attest every recoverable commit since adoption (see --branch)")
	branch := fs.String("branch", "", "branch to sweep with --all (default: current)")
	push := fs.Bool("push", false, "push the re-attestation note to the remote")
	dryRun := fs.Bool("dry-run", false, "report what --all would do and write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "reattest", "commit") {
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	if *all {
		if *dryRun {
			return reattestPlan(svc, *branch, stdout, stderr)
		}
		return reattestAll(svc, *branch, *push, stdout, stderr)
	}
	if *dryRun {
		// Only the sweep has a plan worth previewing; for one commit the answer
		// is the command itself. Saying so beats silently ignoring the flag.
		_, _ = fmt.Fprintln(stderr, "warden: --dry-run applies to --all")
		return 2
	}
	res, err := svc.Reattest(*commit, *push)
	if err != nil {
		return fail(stderr, err)
	}
	switch {
	case res.AlreadyHad:
		_, _ = fmt.Fprintf(stdout, "warden: %s already carries a valid note; nothing to re-attest.\n", short(res.Target))
		return 0
	case res.Wrote:
		suffix := ""
		if *push {
			suffix = " and pushed"
		}
		_, _ = fmt.Fprintf(stdout, "warden: re-attested %s from tree-identical validated %s%s.\n", short(res.Target), short(res.Source), suffix)
		return 0
	default:
		_, _ = fmt.Fprintf(stdout, "warden: no validated commit reproduces %s's tree; not re-attesting.\n", short(res.Target))
		return 1
	}
}

// reattestAll sweeps a branch, closing every recoverable provenance gap in one
// pass — the form that actually gets run after a squash-merge, as opposed to
// remembering a SHA per merge. An empty sweep is success, not failure: it means
// the branch has no recoverable gap, which is the state we want.
func reattestAll(svc *service.Service, branch string, push bool, stdout, stderr io.Writer) int {
	results, err := svc.ReattestAll(branch, push, func(sha string, n, total int) {
		// Per-commit, before the work: a sweep over a trunk is minutes long and
		// silence is indistinguishable from a hang.
		_, _ = fmt.Fprintf(stdout, "warden: [%d/%d] %s\n", n, total, short(sha))
	})
	if err != nil {
		return fail(stderr, err)
	}
	if len(results) == 0 {
		// Say what actually happened. A no-op sweep WITH --push still published
		// any notes an earlier push-less run left local, and reporting only
		// "nothing to re-attest" would leave the reader unsure whether the remote
		// is now current — which is the whole question they ran this to answer.
		msg := "warden: nothing to re-attest; no unverified commit has a validated tree-identical source."
		if push {
			msg += "\nwarden: pushed notes to the remote (no-op if it was already current)."
		}
		_, _ = fmt.Fprintln(stdout, msg)
		return 0
	}
	for _, r := range results {
		_, _ = fmt.Fprintf(stdout, "warden: re-attested %s from tree-identical validated %s.\n", short(r.Target), short(r.Source))
	}
	suffix := "run `warden reattest --all --push` to publish them"
	if push {
		suffix = "pushed to the remote"
	}
	_, _ = fmt.Fprintf(stdout, "warden: %d commit(s) re-attested; %s.\n", len(results), suffix)
	return 0
}

// reattestPlan answers "what would this do" without writing, which omitting
// --push was mistaken for and is not: that still files local notes.
func reattestPlan(svc *service.Service, branch string, stdout, stderr io.Writer) int {
	plan, err := svc.ReattestPlan(branch)
	if err != nil {
		return fail(stderr, err)
	}
	if len(plan) == 0 {
		_, _ = fmt.Fprintln(stdout, "warden: nothing to re-attest; no unverified commit has a validated tree-identical source.")
		return 0
	}
	for _, r := range plan {
		_, _ = fmt.Fprintf(stdout, "warden: would re-attest %s from tree-identical validated %s.\n", short(r.Target), short(r.Source))
	}
	_, _ = fmt.Fprintf(stdout, "warden: %d commit(s) would be re-attested; nothing was written.\n", len(plan))
	return 0
}
