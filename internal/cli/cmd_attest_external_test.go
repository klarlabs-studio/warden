package cli

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// Detection copies facts the platform states about itself — which run, which
// repo. Nothing more.
func TestDetectExternalRun_FillsFromGitHubActions(t *testing.T) {
	var ref domain.ExternalRunRef
	detectExternalRun(&ref, envOf(map[string]string{
		"GITHUB_ACTIONS":      "true",
		"GITHUB_RUN_ID":       "30747937107",
		"GITHUB_RUN_ATTEMPT":  "2",
		"GITHUB_REPOSITORY":   "klarlabs-studio/warden",
		"GITHUB_SERVER_URL":   "https://github.com",
		"GITHUB_SHA":          "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"GITHUB_WORKFLOW_REF": "irrelevant",
	}))

	if ref.Provider != "github-actions" || ref.RunID != "30747937107" || ref.Attempt != 2 {
		t.Errorf("run identity not detected: %+v", ref)
	}
	if ref.Repository != "klarlabs-studio/warden" {
		t.Errorf("repository = %q", ref.Repository)
	}
	if !strings.HasSuffix(ref.URL, "/actions/runs/30747937107") {
		t.Errorf("url = %q", ref.URL)
	}
}

// GITHUB_SHA must NOT become the attested commit.
//
// On a pull_request event it is the merge-PREVIEW commit, not the one being
// attested. Silently attesting the wrong object is the exact failure this design
// refuses, and it would be invisible: the note would verify, against a commit
// nobody asked about.
func TestDetectExternalRun_NeverTakesTheCommitFromTheEnvironment(t *testing.T) {
	var ref domain.ExternalRunRef
	detectExternalRun(&ref, envOf(map[string]string{
		"GITHUB_ACTIONS": "true",
		"GITHUB_RUN_ID":  "1",
		"GITHUB_SHA":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}))
	if ref.Commit != "" {
		t.Errorf("commit = %q; it must come from --commit resolved in the repo, never from the environment", ref.Commit)
	}
}

// An explicit flag always wins, so a platform warden does not know can still be
// used by supplying everything by hand.
func TestDetectExternalRun_ExplicitValuesWin(t *testing.T) {
	ref := domain.ExternalRunRef{Provider: "buildkite", RunID: "abc", Repository: "acme/thing"}
	detectExternalRun(&ref, envOf(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "999",
		"GITHUB_REPOSITORY": "someone/else",
	}))
	if ref.Provider != "buildkite" || ref.RunID != "abc" || ref.Repository != "acme/thing" {
		t.Errorf("detection overwrote explicit values: %+v", ref)
	}
}

// Off a CI platform, detection does nothing rather than inventing a provider.
func TestDetectExternalRun_NoOpOutsideCI(t *testing.T) {
	var ref domain.ExternalRunRef
	detectExternalRun(&ref, envOf(map[string]string{}))
	if ref.Provider != "" || ref.RunID != "" || ref.Repository != "" || ref.URL != "" || ref.Attempt != 0 {
		t.Errorf("detection must be a no-op outside CI: %+v", ref)
	}
}

// --checks is required and never inferred.
//
// Every other field is a fact the platform states; WHAT PASSED is a claim about
// work warden did not watch. Inferring it — from the job succeeding, say — would
// put a warden-signed assertion behind a guess, which is the failure this design
// exists to avoid.
func TestAttestExternal_RefusesWithoutChecks(t *testing.T) {
	gitRepo(t)
	code, _, errb := run("attest-external", "--run-id", "1", "--provider", "github-actions", "--repository", "a/b")
	if code == 0 {
		t.Error("attest-external must not proceed without --checks")
	}
	if !strings.Contains(errb, "will not guess") {
		t.Errorf("the refusal must say why it is not inferred: %q", errb)
	}
}

// A stray positional argument is refused like everywhere else, rather than
// silently attesting HEAD.
func TestAttestExternal_RejectsPositionalArguments(t *testing.T) {
	gitRepo(t)
	code, _, errb := run("attest-external", "abc123")
	if code != 2 || !strings.Contains(errb, "--commit abc123") {
		t.Errorf("code=%d err=%q", code, errb)
	}
}

// End to end through the CLI: attest a commit, then verify it under each
// policy. This is the whole feature in one test — a commit gets an attestation
// naming the run that did the work, and `verify` accepts it only on opt-in.
func TestAttestExternal_AttestsAndIsAcceptedOnlyOnOptIn(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("init", "--hooks=pre-push"); code != 0 {
		t.Fatalf("init: %d %s", code, errb)
	}

	code, out, errb := run("attest-external",
		"--checks", "lint,test",
		"--provider", "github-actions",
		"--run-id", "30747937107",
		"--repository", "klarlabs-studio/warden")
	if code != 0 {
		t.Fatalf("attest-external: %d out=%q err=%q", code, out, errb)
	}
	if !strings.Contains(out, "attested") || !strings.Contains(out, "lint, test") {
		t.Errorf("output must name what it attested and which checks: %q", out)
	}
	// Not published unless asked, and it says which.
	if !strings.Contains(out, "not published") {
		t.Errorf("a run without --push must say the note is unpublished: %q", out)
	}

	// Default policy refuses it…
	if code, _, _ := run("verify"); code == 0 {
		t.Error("plain verify must not accept an external attestation")
	}
	// …and says why, rather than reporting a missing note.
	_, vout, _ := run("verify")
	if !strings.Contains(vout, "EXTERNAL") {
		t.Errorf("the refusal must name the reason: %q", vout)
	}
	// Opting in accepts it.
	if code, vout, verr := run("verify", "--allow-external"); code != 0 {
		t.Errorf("--allow-external must accept it: %d out=%q err=%q", code, vout, verr)
	}
}

// Re-attesting an already-attested commit is a no-op, not an overwrite. A CI job
// that re-runs must not replace the record of what happened the first time.
func TestAttestExternal_SecondRunIsANoOp(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("init", "--hooks=pre-push"); code != 0 {
		t.Fatalf("init: %d %s", code, errb)
	}
	args := []string{"attest-external", "--checks", "lint",
		"--provider", "github-actions", "--run-id", "1", "--repository", "a/b"}
	if code, _, errb := run(args...); code != 0 {
		t.Fatalf("first attest: %d %s", code, errb)
	}

	code, out, _ := run(args...)
	if code != 0 {
		t.Errorf("a second attest must not be an error, got %d", code)
	}
	if !strings.Contains(out, "already carries an attestation") {
		t.Errorf("it must say nothing was written: %q", out)
	}
}

// A malformed --attempt is refused rather than silently dropped: it is part of
// the run's identity, and a wrong attempt number points at the wrong run.
func TestAttestExternal_RejectsANonNumericAttempt(t *testing.T) {
	gitRepo(t)
	code, _, errb := run("attest-external", "--checks", "lint", "--attempt", "two",
		"--provider", "x", "--run-id", "1", "--repository", "a/b")
	if code == 0 || !strings.Contains(errb, "not a number") {
		t.Errorf("code=%d err=%q", code, errb)
	}
}
