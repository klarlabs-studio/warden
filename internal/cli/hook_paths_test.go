package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestStatusReportsHiddenHooksInactive(t *testing.T) {
	dir := gitRepo(t)
	chdir(t, dir)
	if code, out, errOut := run("init"); code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	cmd := exec.Command("git", "config", "core.hooksPath", ".husky/_")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("override: %v %s", err, out)
	}
	code, out, errOut := run("status")
	if code != 0 || !strings.Contains(out, "inactive") || !strings.Contains(out, "core.hooksPath") || strings.Contains(out, "armed") {
		t.Fatalf("hidden hook reported active: %d %s %s", code, out, errOut)
	}
	if code, out, errOut := run("init"); code == 0 || !strings.Contains(errOut, "core.hooksPath") || strings.Contains(out, "initialized") {
		t.Fatalf("init claimed activation: %d %s %s", code, out, errOut)
	}
}
