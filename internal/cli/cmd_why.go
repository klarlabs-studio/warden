package cli

import (
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/service"
)

// cmdWhy handles `warden why [commit]`, explaining what warden did for a past
// commit by reading its provenance note: which rules matched, which steps ran,
// the agent per step, and the signed evidence root. It answers "why did (or
// didn't) the gate do X here" from the record itself — no re-run.
func cmdWhy(args []string, stdout, stderr io.Writer) int {
	commit := "HEAD"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		commit = args[0]
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.Verify(commit)
	if err != nil {
		return fail(stderr, err)
	}
	if res.Record == nil {
		_, _ = fmt.Fprintf(stdout, "no warden note on %s — the commit was made outside the gate or predates adoption.\n", short(res.SHA))
		return 1
	}

	rec := res.Record
	_, _ = fmt.Fprintf(stdout, "commit:        %s\n", short(res.SHA))
	_, _ = fmt.Fprintf(stdout, "run:           %s\n", rec.RunID)
	if rec.Timestamp != "" {
		_, _ = fmt.Fprintf(stdout, "when:          %s\n", rec.Timestamp)
	}
	if rec.WardenVersion != "" {
		_, _ = fmt.Fprintf(stdout, "warden:        %s\n", rec.WardenVersion)
	}

	_, _ = fmt.Fprintf(stdout, "steps run:    ")
	for _, s := range rec.StepsRun {
		_, _ = fmt.Fprintf(stdout, " %s", s)
		if rec.Agent != nil {
			if a := rec.Agent[s]; a != "" {
				_, _ = fmt.Fprintf(stdout, "(agent=%s)", a)
			}
		}
	}
	_, _ = fmt.Fprintln(stdout)

	if len(rec.MatchedRules) > 0 {
		_, _ = fmt.Fprintf(stdout, "matched rules: %s\n", strings.Join(rec.MatchedRules, ", "))
	} else {
		_, _ = fmt.Fprintln(stdout, "matched rules: (none — base policy)")
	}

	_, _ = fmt.Fprintf(stdout, "evidence:      %d records, chain %s\n", len(rec.Evidence), chainState(res.Validated))
	_, _ = fmt.Fprintf(stdout, "signature:     %s\n", whySignature(res))
	if len(rec.Dependencies) > 0 {
		_, _ = fmt.Fprintf(stdout, "sbom:          %d lockfile(s)\n", len(rec.Dependencies))
		for _, m := range rec.Dependencies {
			_, _ = fmt.Fprintf(stdout, "  %-9s %s  %s\n", m.Ecosystem, m.Path, m.Digest)
		}
	}
	return 0
}

func chainState(intact bool) string {
	if intact {
		return "intact"
	}
	return "BROKEN"
}

func whySignature(res service.VerifyResult) string {
	switch {
	case res.SignatureValid:
		return "valid, signed by " + res.Signer
	case res.Signed:
		return "present but INVALID"
	default:
		return "unsigned"
	}
}
