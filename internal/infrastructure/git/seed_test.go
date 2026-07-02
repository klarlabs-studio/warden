package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCreateWorktreeFromHead_TrailingBlankContext guards the seeding bug where
// capturing the staged diff through the trimming run() stripped a hunk's
// trailing blank context lines, corrupting the patch ("corrupt patch at line
// N") when git apply re-checked the line counts. The diff must be captured
// byte-exact.
func TestCreateWorktreeFromHead_TrailingBlankContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "t@t.co")
	gitRun("config", "user.name", "t")

	// A file whose final hunk will carry trailing blank context lines, plus a
	// file with no terminating newline — both patch shapes the trim corrupted.
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("f.txt", "alpha\nbeta\ngamma\n\n\n")
	write("nonl.txt", "x") // no trailing newline
	gitRun("add", ".")
	gitRun("commit", "-m", "init")

	// Stage edits that produce trailing-blank-context and no-newline hunks.
	write("f.txt", "alpha\nBETA\ngamma\n\n\n")
	write("nonl.txt", "xy")
	gitRun("add", ".")

	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.CreateWorktreeFromHead()
	if err != nil {
		t.Fatalf("seeding must not corrupt the staged patch: %v", err)
	}
	defer wt.Remove()

	// The worktree must carry the staged edits.
	got, err := os.ReadFile(filepath.Join(wt.Dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nBETA\ngamma\n\n\n" {
		t.Errorf("seeded worktree missing staged edit: %q", got)
	}
}
