package cli

import (
	"flag"
	"fmt"
	"io"
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
	push := fs.Bool("push", false, "push the re-attestation note to the remote")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
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
