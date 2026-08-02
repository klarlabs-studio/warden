package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdAttestExternal handles `warden attest-external`: record that an external CI
// run executed the checks for a commit (ADR 0003, #177).
//
// It is a SEPARATE command rather than a flag on `warden run`, which the ADR
// first sketched. `run` means "run the hook pipeline"; a flag on it meaning
// "don't run anything" would make the strong and weak claims a runtime
// coin-flip on a shared code path — and keeping them impossible to confuse is
// the entire design.
func cmdAttestExternal(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attest-external", flag.ContinueOnError)
	fs.SetOutput(stderr)
	commit := fs.String("commit", "HEAD", "commit the external run executed against")
	// --checks has no default and no detection, deliberately: see below.
	checks := fs.String("checks", "", "comma-separated names of the checks the external run reported PASSING (required)")
	provider := fs.String("provider", "", "CI platform (default: detected)")
	runID := fs.String("run-id", "", "the platform's run identifier (default: detected)")
	attempt := fs.String("attempt", "", "run attempt number (default: detected)")
	repository := fs.String("repository", "", "owner/name as the platform names it (default: detected)")
	url := fs.String("url", "", "link to the run (default: detected)")
	push := fs.Bool("push", false, "publish the note to the remote")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "attest-external", "commit") {
		return 2
	}

	ref := domain.ExternalRunRef{
		Provider:   *provider,
		RunID:      *runID,
		Repository: *repository,
		URL:        *url,
		Checks:     splitList(*checks),
	}
	if *attempt != "" {
		n, err := strconv.Atoi(*attempt)
		if err != nil {
			return fail(stderr, fmt.Errorf("--attempt %q is not a number", *attempt))
		}
		ref.Attempt = n
	}
	detectExternalRun(&ref, os.Getenv)

	// The checks are the one thing warden cannot detect and must not guess.
	//
	// Everything else here is a fact about the environment — which run, which
	// repo — that the platform states and warden merely copies. WHAT PASSED is a
	// claim about work warden did not watch, and inferring it (from the job
	// succeeding, say) would put a warden-signed assertion behind a guess. That
	// is the failure this whole design exists to avoid, so it is a hard error
	// rather than a default.
	if len(ref.Checks) == 0 {
		return fail(stderr, fmt.Errorf(
			"--checks is required: name the checks the run reported passing. warden did not run them "+
				"and will not guess what they were"))
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.AttestExternal(*commit, ref, *push)
	if err != nil {
		return fail(stderr, err)
	}
	if res.AlreadyHad {
		_, _ = fmt.Fprintf(stdout, "warden: %s already carries an attestation (%s); nothing written\n",
			short(res.SHA), res.RunID)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "warden: attested %s from %s run %s (%s)%s\n",
		short(res.SHA), ref.Provider, ref.RunID, strings.Join(ref.Checks, ", "), pushedSuffix(*push))
	return 0
}

func pushedSuffix(pushed bool) string {
	if pushed {
		return "; published"
	}
	return "; not published (pass --push)"
}

// detectExternalRun fills unset fields from the CI environment.
//
// Only fields the platform states about itself are detected. An explicit flag
// always wins, so a caller on a platform warden does not know can supply
// everything by hand.
func detectExternalRun(ref *domain.ExternalRunRef, getenv func(string) string) {
	if getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	if ref.Provider == "" {
		ref.Provider = "github-actions"
	}
	if ref.RunID == "" {
		ref.RunID = getenv("GITHUB_RUN_ID")
	}
	if ref.Repository == "" {
		ref.Repository = getenv("GITHUB_REPOSITORY")
	}
	if ref.Attempt == 0 {
		if n, err := strconv.Atoi(getenv("GITHUB_RUN_ATTEMPT")); err == nil {
			ref.Attempt = n
		}
	}
	if ref.URL == "" && getenv("GITHUB_SERVER_URL") != "" && ref.Repository != "" && ref.RunID != "" {
		ref.URL = getenv("GITHUB_SERVER_URL") + "/" + ref.Repository + "/actions/runs" + "/" + ref.RunID
	}
	// GITHUB_SHA is deliberately NOT used to fill ref.Commit. On a pull_request
	// event it is the merge-preview commit, not the one being attested, and
	// silently attesting the wrong object is the exact failure this design
	// refuses. The commit comes from --commit (default HEAD), resolved in the
	// repository where the note will be written.
}
