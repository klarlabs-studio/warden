package cli

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// run invokes the dispatcher with the given args (argv[0] implied) and returns
// exit code plus captured stdout/stderr.
func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(append([]string{"warden"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestDispatch_VersionHelpUnknown(t *testing.T) {
	if code, out, _ := run("version"); code != 0 || !strings.Contains(out, "warden") {
		t.Errorf("version: code=%d out=%q", code, out)
	}
	if code, out, _ := run("help"); code != 0 || !strings.Contains(out, "Usage") {
		t.Errorf("help: code=%d out=%q", code, out)
	}
	if code, _, errb := run("bogus"); code != 2 || !strings.Contains(errb, "unknown command") {
		t.Errorf("unknown: code=%d err=%q", code, errb)
	}
	if code, _, _ := run(); code != 0 {
		t.Errorf("no args should print help and exit 0, got %d", code)
	}
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestDispatch_InitStepsPolicyInRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t.co"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	chdir(t, dir)

	if code, _, errb := run("init", "--hooks=pre-push"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}
	if code, out, _ := run("steps", "list"); code != 0 || !strings.Contains(out, "pre-push") {
		t.Errorf("steps list: code=%d out=%q", code, out)
	}
	if code, out, _ := run("policy", "explain", "--hook", "pre-push"); code != 0 || !strings.Contains(out, "steps:") {
		t.Errorf("policy explain: code=%d out=%q", code, out)
	}
	if code, out, _ := run("policy", "explain", "--hook", "pre-push", "--chart"); code != 0 || !strings.Contains(out, "states") {
		t.Errorf("policy explain --chart: code=%d out=%q", code, out)
	}
}

func TestDispatch_BadHookErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Outside a repo, hook commands should error cleanly (non-zero), not panic.
	dir := t.TempDir()
	chdir(t, dir)
	if code, _, _ := run("run", "not-a-hook"); code == 0 {
		t.Error("run with a bad hook should be non-zero")
	}
}
