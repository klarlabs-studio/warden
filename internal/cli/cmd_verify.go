package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
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
	rangeSpec := fs.String("range", "", "gate every commit in a BASE..HEAD range (e.g. origin/main..HEAD); exits non-zero if any commit lacks trusted provenance")
	requireSigned := fs.Bool("require-signed", false, "require each note to carry a signature that verifies (implied by --key)")
	skipMerges := fs.Bool("skip-merges", true, "in --range, skip merge commits (their parents are gated individually)")
	jsonOut := fs.Bool("json", false, "in --range, emit per-commit verdicts as JSON")
	quiet := fs.Bool("quiet", false, "print nothing; communicate only via exit code")
	keys := fs.String("key", "", "comma-separated trusted signer key(s) or fingerprint(s); require a matching signature")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}

	if *rangeSpec != "" {
		return runVerifyRange(svc, *rangeSpec, service.RangeVerifyOptions{
			RequireSigned: *requireSigned,
			TrustedKeys:   splitList(*keys),
			SkipMerges:    *skipMerges,
		}, *jsonOut, *quiet, stdout, stderr)
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

// runVerifyRange gates a BASE..HEAD range: it exits 0 only when every commit
// carries provenance to the required depth, and non-zero (with per-commit
// reasons) otherwise. This is the primitive a PR required-check or a
// pre-receive hook wraps — see docs/adr/0002.
func runVerifyRange(svc *service.Service, spec string, opts service.RangeVerifyOptions, jsonOut, quiet bool, stdout, stderr io.Writer) int {
	base, head, ok := parseRange(spec)
	if !ok {
		fmt.Fprintf(stderr, "warden: --range must be BASE..HEAD (two-dot), e.g. origin/main..HEAD; got %q\n", spec)
		return 2
	}
	res, err := svc.VerifyRange(base, head, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if !quiet {
		if jsonOut {
			if code := printRangeJSON(stdout, stderr, res, opts); code != 0 {
				return code
			}
		} else {
			printRange(stdout, res, opts)
		}
	}
	if res.OK() {
		return 0
	}
	return 1
}

// parseRange splits a two-dot "BASE..HEAD" spec. It rejects the three-dot
// symmetric-difference form (git's `A...B`) — a provenance gate must walk a
// definite ancestry range, not a symmetric diff — and any spec missing an
// endpoint.
func parseRange(spec string) (base, head string, ok bool) {
	if strings.Contains(spec, "...") {
		return "", "", false
	}
	b, h, found := strings.Cut(spec, "..")
	if !found || b == "" || h == "" {
		return "", "", false
	}
	return b, h, true
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

// gateDepth names, for the human summary, how strict this gate was — so a green
// result is not mistaken for more assurance than it checked.
func gateDepth(opts service.RangeVerifyOptions) string {
	switch {
	case len(opts.TrustedKeys) > 0:
		return "trusted-signed"
	case opts.RequireSigned:
		return "signed"
	default:
		return "attested"
	}
}

// reasonHint turns a machine reason into a one-line explanation for the human
// report.
func reasonHint(r domain.VerifyReason) string {
	switch r {
	case domain.ReasonMissing:
		return "no warden note (pushed with --no-verify, or made outside warden)"
	case domain.ReasonBrokenChain:
		return "note present but does not attest this commit (broken/transplanted)"
	case domain.ReasonUnsigned:
		return "note is unsigned or its signature does not verify"
	case domain.ReasonUntrusted:
		return "signed by a key outside the trusted set"
	default:
		return "ok"
	}
}

// printRange renders the human-readable range-gate result: a per-failure line
// for anything that did not pass, then a one-line verdict naming the depth.
func printRange(w io.Writer, res service.RangeVerifyResult, opts service.RangeVerifyOptions) {
	fails := res.Failures()
	for _, v := range fails {
		fmt.Fprintf(w, "  ✗ %s — %s\n", short(v.SHA), reasonHint(v.Reason))
	}
	if len(fails) == 0 {
		fmt.Fprintf(w, "verified %d commit(s) in %s..%s (%s)\n", len(res.Commits), short(res.Base), short(res.Head), gateDepth(opts))
		return
	}
	fmt.Fprintf(w, "FAILED: %d of %d commit(s) in %s..%s lack %s provenance\n", len(fails), len(res.Commits), short(res.Base), short(res.Head), gateDepth(opts))
}

// rangeExport is the stable JSON shape a CI job or the Phase-2 action consumes.
type rangeExport struct {
	Base    string                 `json:"base"`
	Head    string                 `json:"head"`
	Depth   string                 `json:"depth"`
	OK      bool                   `json:"ok"`
	Total   int                    `json:"total"`
	Failed  int                    `json:"failed"`
	Commits []domain.CommitVerdict `json:"commits"`
}

// printRangeJSON emits the range result as JSON. It returns a non-zero CLI code
// only on an encode failure; the pass/fail exit is decided by the caller from
// res.OK so --json and text share one exit contract.
func printRangeJSON(stdout, stderr io.Writer, res service.RangeVerifyResult, opts service.RangeVerifyOptions) int {
	out := rangeExport{
		Base:    res.Base,
		Head:    res.Head,
		Depth:   gateDepth(opts),
		OK:      res.OK(),
		Total:   len(res.Commits),
		Failed:  len(res.Failures()),
		Commits: res.Commits,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fail(stderr, err)
	}
	return 0
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
