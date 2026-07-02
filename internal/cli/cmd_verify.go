package cli

import (
	"flag"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/service"
)

// cmdVerify handles `warden verify`, the CI provenance-skip primitive. It exits
// 0 when the commit carries an intact warden validation note (CI can trust it
// and skip re-running the checks warden already ran) and non-zero otherwise
// (CI should run the checks). Designed for: `warden verify && exit 0 || make ci`.
// With --key, the note must also be signed by a pinned trusted key, turning
// provenance-skip from "a warden ran here" into "a warden I trust ran here".
func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.String("commit", "HEAD", "commit to verify")
	quiet := fs.Bool("quiet", false, "print nothing; communicate only via exit code")
	keys := fs.String("key", "", "comma-separated trusted signer key(s) or fingerprint(s); require a matching signature")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.Verify(*commit, splitList(*keys)...)
	if err != nil {
		return fail(stderr, err)
	}

	if !*quiet {
		printVerify(stdout, res, *keys != "")
	}
	if res.Validated {
		return 0
	}
	return 1
}

// printVerify renders a verify result, including signature provenance.
func printVerify(w io.Writer, res service.VerifyResult, pinned bool) {
	if res.Validated {
		fmt.Fprintf(w, "validated %s", short(res.SHA))
		if res.Record != nil {
			fmt.Fprintf(w, " (%s, %d steps, chain-intact", res.Record.RunID, len(res.Record.StepsRun))
			fmt.Fprintf(w, "%s)", signerNote(res))
		}
		fmt.Fprintln(w)
		return
	}
	// Give a specific reason when a pinned key is what failed.
	if pinned && res.Signed && res.SignatureValid && !res.Trusted {
		fmt.Fprintf(w, "unverified %s — signed by untrusted key %s; run the checks\n", short(res.SHA), res.Signer)
		return
	}
	if pinned && res.Signed && !res.SignatureValid {
		fmt.Fprintf(w, "unverified %s — signature does not verify; run the checks\n", short(res.SHA))
		return
	}
	if pinned && !res.Signed {
		fmt.Fprintf(w, "unverified %s — note is unsigned but a trusted key was required; run the checks\n", short(res.SHA))
		return
	}
	fmt.Fprintf(w, "unverified %s — no intact warden note; run the checks\n", short(res.SHA))
}

// signerNote describes the signature state for a validated line.
func signerNote(res service.VerifyResult) string {
	switch {
	case res.Trusted:
		return fmt.Sprintf(", signed by trusted %s", res.Signer)
	case res.SignatureValid:
		return fmt.Sprintf(", signed by %s", res.Signer)
	case res.Signed:
		return ", signature INVALID"
	default:
		return ", unsigned"
	}
}
