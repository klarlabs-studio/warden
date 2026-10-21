package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCmdMCP_Usage covers the argument guard: `warden mcp` needs the `serve`
// subcommand, and anything else is a usage error (exit 2) rather than starting
// the stdio server.
func TestCmdMCP_Usage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}} {
		var out, errb bytes.Buffer
		if rc := cmdMCP(args, &out, &errb); rc != 2 {
			t.Errorf("cmdMCP(%q): rc=%d, want 2", args, rc)
		}
		if !strings.Contains(errb.String(), "usage: warden mcp serve") {
			t.Errorf("cmdMCP(%q): missing usage message, got %q", args, errb.String())
		}
	}
}

// TestCmdAxi_ErrorPaths exercises the axi dispatcher's usage/error branches
// (no server, no command execution): usage, unknown verb, bad flags, and an
// invalid --hook that fails ParseHook after the trust gate.
//
// It runs in a repository of its own. cmdAxi opens the repository at the
// working directory before dispatching, so a test that borrowed whatever
// directory the process happened to be in would pass these "must fail" cases
// for the wrong reason whenever that directory was not a repository.
func TestCmdAxi_ErrorPaths(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	repoWithConfig(t, "")
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown verb", []string{"frobnicate"}},
		{"policy-explain bad flag", []string{"policy-explain", "--nope"}},
		{"policy-explain bad hook", []string{"policy-explain", "--hook", "bogus"}},
		{"run-trigger bad flag", []string{"run-trigger", "--nope"}},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if rc := cmdAxi(c.args, &out, &errb); rc == 0 {
			t.Errorf("cmdAxi(%s): rc=0, want non-zero error", c.name)
		}
	}

	// run-trigger with an invalid hook, past the trust gate (env opt-in): it must
	// fail at hook parsing, never reaching command execution.
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "1")
	var out, errb bytes.Buffer
	if rc := cmdAxi([]string{"run-trigger", "--hook", "bogus"}, &out, &errb); rc == 0 {
		t.Error("run-trigger with a bogus hook must fail, not run")
	}
	if !strings.Contains(errb.String(), `unknown hook "bogus"`) {
		t.Errorf("run-trigger must fail at hook parsing, got %q", errb.String())
	}
}

// TestCmdAxi_RunTriggerRefusedWithoutTrust confirms run-trigger refuses to
// execute repo commands unless explicitly trusted.
//
// The repository and the trust store are the test's own. Borrowing the process
// working directory made the outcome depend on where the suite was started (and
// on whether an earlier test had left it somewhere that no longer exists), and
// reading the operator's real allowlist would make the refusal vanish on any
// machine that had trusted the checkout under test — at which point the test
// would run that checkout's pre-push pipeline.
func TestCmdAxi_RunTriggerRefusedWithoutTrust(t *testing.T) {
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "")
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	repoWithConfig(t, "")
	var out, errb bytes.Buffer
	if rc := cmdAxi([]string{"run-trigger"}, &out, &errb); rc == 0 {
		t.Error("run-trigger without trust must refuse (non-zero)")
	}
	if !strings.Contains(errb.String(), "trust") {
		t.Errorf("refusal should mention trust, got %q", errb.String())
	}
}

// TestCmdAxi_IndependentOfTheStartingDirectory re-runs the axi tests in a child
// test binary whose starting directory is deleted before any test runs.
//
// That is the shape of an intermittent failure seen in the pre-push gate: the
// directory the suite started in went away mid-run, the old chdir helper's
// restore failed silently, every later test was left in the previous test's
// already-deleted temp dir, and TestCmdAxi_RunTriggerRefusedWithoutTrust —
// which borrowed the process working directory instead of making a repository
// — reported "is not a repository" where it expected a trust refusal. TestKeyList
// runs first because it is the test that left the directory behind in that run.
func TestCmdAxi_IndependentOfTheStartingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process's working directory cannot be removed on Windows")
	}
	if os.Getenv(envVanishStartDir) != "" {
		t.Skip("already running as the child")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(t.TempDir(), vanishStartPrefix+"start")
	if err := os.Mkdir(start, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.v", "-test.count=1",
		"-test.run", "^(TestKeyList|TestCmdAxi_ErrorPaths|TestCmdAxi_RunTriggerRefusedWithoutTrust)$")
	cmd.Dir = start
	cmd.Env = append(os.Environ(), envVanishStartDir+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("axi tests failed from a vanished starting directory: %v\n%s", err, out)
	}
	if _, err := os.Stat(start); !os.IsNotExist(err) {
		t.Fatalf("the child did not remove its starting directory (stat: %v); the test proved nothing", err)
	}
	for _, name := range []string{"TestKeyList", "TestCmdAxi_ErrorPaths", "TestCmdAxi_RunTriggerRefusedWithoutTrust"} {
		if !strings.Contains(string(out), "--- PASS: "+name+" ") {
			t.Errorf("child did not pass %s:\n%s", name, out)
		}
	}
}
