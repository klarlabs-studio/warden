package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// gatedRepo builds a repo with a bare remote, adopts warden, and pushes one
// commit THROUGH the gate — so it carries a real validation note. The provenance
// verbs only reach their interesting branches (a record to project, commits to
// classify) against a repo that has actually been gated; an un-adopted repo
// exercises only the empty answers.
func gatedRepo(t *testing.T) {
	t.Helper()
	dir := gitRepo(t)
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("remote", "add", "origin", remote)
	git("branch", "-M", "main")
	git("push", "--no-verify", "-u", "origin", "main")

	writeConfig(t, dir, `hooks: { pre_push: true }
steps: { pre_push: [lint] }
commands: { lint: "true" }
rules: []
`)
	if code, _, errb := run("init"); code != 0 {
		t.Fatalf("init: %d %q", code, errb)
	}
	git("commit", "--allow-empty", "--no-verify", "-m", "gated change")
	if code, _, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %q", code, errb)
	}
	// Run the gate for real: this writes the provenance note the verbs read.
	if code, out, errb := run("run", "pre-push"); code != 0 {
		t.Fatalf("gate run: code=%d out=%q err=%q", code, out, errb)
	}
	// The gate passed without rewriting anything, so it stood aside for git to
	// perform the push — but this called the hook entry point directly, so no
	// git push ever followed and origin/main is still at the adoption point.
	// doctor and audit walk adoption..origin/<branch>, so publish it here or they
	// report an empty range against a repo that really was gated.
	git("push", "--no-verify", "origin", "main")
}

// Against a genuinely gated commit, verify reports validated and projects the
// note's own fields — what the gate ACTUALLY did, rather than what current
// policy would do.
func TestAxi_VerifyOnAGatedCommit(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gatedRepo(t)

	code, out, errb := run("axi", "verify")
	if code != 0 {
		t.Fatalf("verify on a gated commit must exit 0: code=%d out=%q err=%q", code, out, errb)
	}
	for _, want := range []string{"validated", "run_id", "steps", "warden_version"} {
		if !strings.Contains(out, want) {
			t.Errorf("verify output missing %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "validated: true") {
		t.Errorf("expected validated: true, got %q", out)
	}

	// Pinning a key that did not sign must refuse: this is the difference
	// between "a warden ran here" and "a warden I trust ran here".
	if code, _, _ := run("axi", "verify", "--key", "0000000000000000"); code == 0 {
		t.Error("verify with an untrusted pinned key must not pass")
	}
}

// A range whose commits are gated passes; the per-commit verdicts are reported
// either way so a caller can say exactly which commit failed and why.
func TestAxi_VerifyRangeOnAGatedRepo(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gatedRepo(t)

	code, out, _ := run("axi", "verify-range", "--base", "HEAD~1")
	if !strings.Contains(out, "commits") {
		t.Errorf("verify-range must report per-commit verdicts: %q", out)
	}
	if code != 0 {
		t.Errorf("a gated range should pass: code=%d out=%q", code, out)
	}

	// --require-signed escalates the gate; the effective depth is reported.
	if _, out, _ := run("axi", "verify-range", "--base", "HEAD~1", "--require-signed"); !strings.Contains(out, "require_signed: true") {
		t.Errorf("the enforced gate depth must be reported: %q", out)
	}
}

// doctor/audit over a gated repo exercise the per-commit reporting path, and
// must agree on the schema.
func TestAxi_DoctorAndAuditOverGatedCommits(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gatedRepo(t)

	for _, verb := range []string{"doctor", "audit"} {
		code, out, errb := run("axi", verb)
		if code != 0 {
			t.Fatalf("axi %s: code=%d err=%q", verb, code, errb)
		}
		for _, want := range []string{"commits", "verified", "chain_intact"} {
			if !strings.Contains(out, want) {
				t.Errorf("axi %s missing %q: %q", verb, want, out)
			}
		}
	}

	// An explicit --branch must be honored, not silently ignored.
	if code, _, errb := run("axi", "doctor", "--branch", "main"); code != 0 {
		t.Errorf("axi doctor --branch: code=%d err=%q", code, errb)
	}
}

// A FAILING run must emit its findings, with file/line when the step reported
// them. Reporting only that something failed is what forced an agent back to an
// interactive run to discover what.
func TestAxi_RunTriggerEmitsFindings(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, `hooks: { pre_commit: true }
steps: { pre_commit: [lint] }
commands: { lint: "echo boom >&2; exit 1" }
rules: []
`)
	if code, _, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %q", code, errb)
	}

	_, out, _ := run("axi", "run-trigger", "--hook", "pre-commit")
	for _, want := range []string{"outcome", "findings", "blocker", "retryable"} {
		if !strings.Contains(out, want) {
			t.Errorf("a failed run must report %q: %q", want, out)
		}
	}
	// A verdict about the CHANGE carries no blocker and is not retryable —
	// re-running an unchanged tree produces the identical failure.
	if !strings.Contains(out, "retryable: false") {
		t.Errorf("a code-verdict failure must not be reported as retryable: %q", out)
	}
	if !strings.Contains(out, "severity") {
		t.Errorf("findings must carry their severity: %q", out)
	}
}
