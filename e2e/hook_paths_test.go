package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build a gate that always rejects, then independently observe whether Git
// invokes it. Status must agree with the real Git operation, not merely the
// existence of the shim Warden previously installed.
func TestGoldenFleet_HookPathOverrideIsNotArmed(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g.dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".warden.yaml", "hooks: { pre_commit: true }\ncommands: { lint: 'false' }\nsteps: { pre_commit: [lint] }\nrules: []\n")
	if out, code := g.warden("init", "--hooks=pre-commit"); code != 0 {
		t.Fatalf("init: %d %s", code, out)
	}
	if err := os.MkdirAll(filepath.Join(g.dir, ".husky", "_"), 0o755); err != nil {
		t.Fatal(err)
	}
	g.git("config", "core.hooksPath", ".husky/_")
	write("input", "unguarded")
	g.git("add", "input", ".warden.yaml")
	commit := func(message string) (string, error) {
		cmd := exec.Command("git", "commit", "-m", message)
		cmd.Dir = g.dir
		cmd.Env = hookEnv()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	head := func() string {
		t.Helper()
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = g.dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	before := head()
	if out, err := commit("Git did not invoke the rejecting gate"); err != nil {
		t.Fatalf("override fixture did not bypass gate: %v %s", err, out)
	}
	accepted := head()
	if accepted == before {
		t.Fatal("unguarded commit was not created")
	}
	if out, code := g.warden("status"); code != 0 || !strings.Contains(out, "inactive") || !strings.Contains(out, "core.hooksPath") || strings.Contains(out, "armed") {
		t.Fatalf("status disagrees with actual Git behavior: %d %s", code, out)
	}
	if out, code := g.warden("init"); code == 0 || !strings.Contains(out, "core.hooksPath") {
		t.Fatalf("init claimed to re-arm hidden hooks: %d %s", code, out)
	}
	// A deliberate path selection makes the very same rejecting shim reachable.
	g.git("config", "core.hooksPath", ".git/hooks")
	write("input", "guarded")
	g.git("add", "input")
	if out, err := commit("the active gate must reject"); err == nil || !strings.Contains(out, "lint") {
		t.Fatalf("active gate did not reject: %v %s", err, out)
	}
	if head() != accepted {
		t.Fatal("rejecting gate allowed a new commit")
	}
	if out, code := g.warden("status"); code != 0 || !strings.Contains(out, "armed") || strings.Contains(out, "inactive") {
		t.Fatalf("active status disagrees with rejecting gate: %d %s", code, out)
	}
}
