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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	if *all {
		return reattestAll(svc, *branch, *push, stdout, stderr)
	}
	res, err := svc.Reattest(*commit, *push)
	if err != nil {
		return fail(stderr, err)
	}
	switch {
	case res.AlreadyHad:
		fmt.Fprintf(stdout, "warden: %s already carries a valid note; nothing to re-attest.\n", short(res.Target))
		return 0
	case res.Wrote:
		suffix := ""
		if *push {
			suffix = " and pushed"
		}
		fmt.Fprintf(stdout, "warden: re-attested %s from tree-identical validated %s%s.\n", short(res.Target), short(res.Source), suffix)
		return 0
	default:
		fmt.Fprintf(stdout, "warden: no validated commit reproduces %s's tree; not re-attesting.\n", short(res.Target))
		return 1
	}
}

// reattestAll sweeps a branch, closing every recoverable provenance gap in one
// pass — the form that actually gets run after a squash-merge, as opposed to
// remembering a SHA per merge. An empty sweep is success, not failure: it means
// the branch has no recoverable gap, which is the state we want.
func reattestAll(svc *service.Service, branch string, push bool, stdout, stderr io.Writer) int {
	results, err := svc.ReattestAll(branch, push)
	if err != nil {
		return fail(stderr, err)
	}
	if len(results) == 0 {
		fmt.Fprintln(stdout, "warden: nothing to re-attest; no unverified commit has a validated tree-identical source.")
		return 0
	}
	for _, r := range results {
		fmt.Fprintf(stdout, "warden: re-attested %s from tree-identical validated %s.\n", short(r.Target), short(r.Source))
	}
	suffix := "run `warden reattest --all --push` to publish them"
	if push {
		suffix = "pushed to the remote"
	}
	fmt.Fprintf(stdout, "warden: %d commit(s) re-attested; %s.\n", len(results), suffix)
	return 0
}
