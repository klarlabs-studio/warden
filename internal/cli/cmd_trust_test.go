package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of the per-repo allowlist: granting trust to THIS repo must
// unblock run-trigger here, and a repo that was never granted must stay refused.
// The env var it supersedes could not express that distinction — it authorized
// the process, so one grant covered every repo the process was later pointed at.
func TestTrust_GrantUnblocksRunTriggerForThatRepoOnly(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)

	// Ungranted: refused, and the refusal names the fix.
	code, _, errb := run("axi", "run-trigger", "--hook", "pre-commit")
	if code == 0 {
		t.Fatal("an ungranted repo must refuse run-trigger")
	}
	if !strings.Contains(errb, "warden trust add") {
		t.Errorf("the refusal must name the per-repo grant, got %q", errb)
	}

	// Grant it.
	if code, out, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add: code=%d err=%q out=%q", code, errb, out)
	}

	// Now permitted, with no env var and no --trust flag.
	if code, _, errb := run("axi", "run-trigger", "--hook", "pre-commit"); code != 0 {
		t.Errorf("a granted repo must permit run-trigger: code=%d err=%q", code, errb)
	}

	// A DIFFERENT repo must remain refused — this is the leak the env var had.
	other := gitRepo(t)
	writeConfig(t, other, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	if code, _, _ := run("axi", "run-trigger", "--hook", "pre-commit"); code == 0 {
		t.Error("trusting one repo must not trust another")
	}
}

// Revoking must actually revoke.
func TestTrust_RemoveRevokes(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	if code, _, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %q", code, errb)
	}
	if code, _, _ := run("axi", "run-trigger", "--hook", "pre-commit"); code != 0 {
		t.Fatal("precondition: the grant should permit run-trigger")
	}

	if code, _, errb := run("trust", "remove"); code != 0 {
		t.Fatalf("trust remove: %d %q", code, errb)
	}
	if code, _, _ := run("axi", "run-trigger", "--hook", "pre-commit"); code == 0 {
		t.Error("run-trigger must be refused again after revoke")
	}
}

// `trust list` reports the granted repos, and says so plainly when there are
// none — an empty allowlist is the healthy default, not an error.
func TestTrust_List(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)

	code, out, _ := run("trust", "list")
	if code != 0 {
		t.Fatalf("trust list on an empty store: code=%d", code)
	}
	if !strings.Contains(out, "no trusted repositories") {
		t.Errorf("empty list output: %q", out)
	}

	if code, _, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %q", code, errb)
	}
	code, out, _ = run("trust", "list")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Errorf("trust list after a grant: code=%d out=%q", code, out)
	}
}

// The env var still works: in a container or CI job there is no persistent
// config dir to hold an allowlist, and the workspace is disposable anyway.
func TestTrust_EnvVarStillGrants(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "1")
	if code, _, errb := run("axi", "run-trigger", "--hook", "pre-commit"); code != 0 {
		t.Errorf("the env var must still grant: code=%d err=%q", code, errb)
	}
}

// A grant made from a SUBDIRECTORY must record the repository root, so the check
// — which also resolves to the root — finds it. Recording the subdirectory
// instead would produce a grant that never matches.
func TestTrust_GrantsAgainstTheRepoRoot(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, sub)

	if code, _, errb := run("trust", "add"); code != 0 {
		t.Fatalf("trust add from a subdirectory: %d %q", code, errb)
	}
	chdir(t, dir)
	if code, _, errb := run("axi", "run-trigger", "--hook", "pre-commit"); code != 0 {
		t.Errorf("a grant made in a subdirectory must apply at the repo root: code=%d err=%q", code, errb)
	}
}

func TestTrust_UsageErrors(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)
	if code, _, errb := run("trust"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("bare trust: code=%d err=%q", code, errb)
	}
	if code, _, errb := run("trust", "bogus"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("unknown subcommand: code=%d err=%q", code, errb)
	}
}
