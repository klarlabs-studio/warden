package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

func cmdCIPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ci plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("policy", "", "protected CI job policy JSON file")
	commit := fs.String("commit", "HEAD", "commit to plan")
	keys := fs.String("key", "", "explicit protected trusted signer keys or fingerprints")
	jsonOut := fs.Bool("json", false, "emit the job plan as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "ci plan", "") {
		return 2
	}
	trusted := splitList(*keys)
	if *file == "" || len(trusted) == 0 || strings.TrimSpace(*commit) == "" {
		_, _ = fmt.Fprintln(stderr, "warden: ci plan requires --policy, explicit --key and a non-empty commit")
		return 2
	}
	var policy domain.CIJobPolicy
	if err := loadProtectedJSON(*file, &policy); err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: invalid CI job policy: %v\n", err)
		return 2
	}
	if err := policy.Validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: invalid CI job policy: %v\n", err)
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	plan, err := svc.PlanCI(*commit, policy, trusted...)
	if err != nil {
		return fail(stderr, err)
	}
	if *jsonOut {
		if err := json.NewEncoder(stdout).Encode(plan); err != nil {
			return fail(stderr, err)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "CI plan for %s (verification %dms)\n", plan.SHA, plan.VerificationMS)
		for _, job := range plan.Jobs {
			_, _ = fmt.Fprintf(stdout, "%s: %s — %s\n", job.Name, job.Action, strings.Join(job.Reasons, "; "))
		}
	}
	// Success means a complete plan, including jobs that must run. It is not
	// an attestation and must never be used as an all-jobs-skip verdict.
	return 0
}
