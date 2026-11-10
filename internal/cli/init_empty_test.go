package cli

import (
	"os/exec"
	"strings"
	"testing"
)

// emptyGitRepo is `git init` and nothing else: where a new project starts.
func emptyGitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "warden-test")
	git("config", "user.name", "t")
	git("config", "commit.gpgsign", "false")
	t.Chdir(dir)
	return dir, git
}

// The first-run path, end to end through the CLI: what a person who types
// `git init && warden init` is told at each step. Every surface that prints
// the adoption point has to say what it is — the stored value is a sentinel,
// and a sentinel run through the SHA abbreviator reads as a commit named
// "root".
func TestInit_EmptyRepositorySaysWhatItAdopted(t *testing.T) {
	_, git := emptyGitRepo(t)

	code, out, errb := run("init")
	if code != 0 {
		t.Fatalf("init in an empty repository: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "adopted from the first commit") {
		t.Errorf("init does not say the audit starts at the first commit: %q", out)
	}
	if strings.Contains(out, "recorded at root") {
		t.Errorf("init printed the sentinel as if it were a commit: %q", out)
	}

	const label = "the first commit (inclusive)"
	if code, out, errb := run("status"); code != 0 || !strings.Contains(out, label) {
		t.Errorf("status: code=%d out=%q err=%q, want the adoption point described as %q", code, out, errb, label)
	}

	// Nothing to audit yet is an answer, not a git error.
	code, _, errb = run("doctor")
	if code == 0 || !strings.Contains(errb, "no commits yet") {
		t.Errorf("doctor before the first commit: code=%d err=%q", code, errb)
	}
	if strings.Contains(errb, "unknown revision") || strings.Contains(errb, "ambiguous argument") {
		t.Errorf("doctor leaked git's complaint: %q", errb)
	}

	git("commit", "-q", "--no-verify", "--allow-empty", "-m", "first")
	code, out, errb = run("doctor")
	if code != 0 {
		t.Fatalf("doctor after the first commit: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "since adoption "+label) {
		t.Errorf("doctor header does not describe the adoption point: %q", out)
	}
	// The root commit is the only commit, so an audit that lists none has
	// dropped it.
	if !strings.Contains(out, "first") {
		t.Errorf("doctor does not list the root commit: %q", out)
	}

	// A second init keeps the point and still names it properly.
	code, out, errb = run("init")
	if code != 0 || !strings.Contains(out, "adoption point kept at "+label) {
		t.Errorf("second init: code=%d out=%q err=%q", code, out, errb)
	}
}
