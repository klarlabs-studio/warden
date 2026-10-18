package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TreeState is what the agent tree guard compares. Untracked output must not
// move it; a tracked edit, a staged change and an amend each must.
func TestWorktree_TreeState(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wt.Remove() }()
	in := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wt.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	state := func() string {
		t.Helper()
		s, err := wt.TreeState()
		if err != nil {
			t.Fatal(err)
		}
		return s.Head + "/" + s.Index + "/" + s.Unstaged
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt.Dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	base := state()
	write("generated.txt", "untracked output\n")
	if got := state(); got != base {
		t.Fatalf("untracked output moved the state: %s -> %s", base, got)
	}
	write("README.md", "edited\n")
	edited := state()
	if edited == base {
		t.Fatal("a tracked edit did not move the state")
	}
	in("add", "README.md")
	staged := state()
	if staged == edited || staged == base {
		t.Fatal("staging did not move the index part of the state")
	}
	in("-c", "user.name=t", "-c", "user.email=t", "-c", "commit.gpgsign=false", "commit", "-q", "--amend", "-m", "amended")
	if got := state(); got == staged || got == base {
		t.Fatal("an amend did not move the state")
	}
}
