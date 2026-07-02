package cli

import (
	"flag"
	"fmt"
	"io"
)

// cmdVerify handles `warden verify`, the CI provenance-skip primitive. It exits
// 0 when the commit carries an intact warden validation note (CI can trust it
// and skip re-running the checks warden already ran) and non-zero otherwise
// (CI should run the checks). Designed for: `warden verify && exit 0 || make ci`.
func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.String("commit", "HEAD", "commit to verify")
	quiet := fs.Bool("quiet", false, "print nothing; communicate only via exit code")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.Verify(*commit)
	if err != nil {
		return fail(stderr, err)
	}

	if !*quiet {
		if res.Validated {
			fmt.Fprintf(stdout, "validated %s", short(res.SHA))
			if res.Record != nil {
				fmt.Fprintf(stdout, " (%s, %d steps, chain-intact)", res.Record.RunID, len(res.Record.StepsRun))
			}
			fmt.Fprintln(stdout)
		} else {
			fmt.Fprintf(stdout, "unverified %s — no intact warden note; run the checks\n", short(res.SHA))
		}
	}
	if res.Validated {
		return 0
	}
	return 1
}
