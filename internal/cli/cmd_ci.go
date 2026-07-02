package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdCI handles `warden ci`, reporting the CI check status for a branch's pull
// request (§4.3 step 3). With --wait it polls until the checks reach a terminal
// state or the timeout elapses — the on-demand counterpart to the async CI
// polling the pipeline does not perform itself.
func cmdCI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branch := fs.String("branch", "", "branch to check (default: current)")
	wait := fs.Bool("wait", false, "poll until checks finish")
	timeout := fs.Duration("timeout", 10*time.Minute, "max time to wait with --wait")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}

	ctx := context.Background()
	deadline := time.Now().Add(*timeout)
	for {
		status, err := svc.CIStatus(ctx, *branch)
		if err != nil {
			return fail(stderr, err)
		}
		printCI(stdout, status)

		if !*wait || isTerminal(status.State) {
			return ciExit(status.State)
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(stderr, "warden: timed out waiting for CI")
			return 1
		}
		time.Sleep(10 * time.Second)
	}
}

func isTerminal(s domain.CIState) bool {
	return s == domain.CIPassing || s == domain.CIFailing || s == domain.CINone
}

func ciExit(s domain.CIState) int {
	if s == domain.CIFailing {
		return 1
	}
	return 0
}

func printCI(w io.Writer, s domain.CIStatus) {
	fmt.Fprintf(w, "CI %s — %d checks: %d passed, %d failed, %d pending\n",
		s.State, s.Total, s.Passed, s.Failed, s.Pending)
}
