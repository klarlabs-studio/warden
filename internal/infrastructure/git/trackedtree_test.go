package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TrackedTree is the tree a commit of the worktree would get: staged changes
// and unstaged edits of tracked files, never untracked files, and without
// touching the worktree's own index.
func TestTrackedTree(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wt.Remove() }()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wt.Dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt.Dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	clean, err := TrackedTree(wt.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if head := git("rev-parse", "HEAD^{tree}"); clean != head {
		t.Fatalf("a clean worktree's tracked tree %s, want HEAD's %s", clean, head)
	}

	write("staged.txt", "staged\n")
	git("add", "staged.txt")
	write("README.md", "edited, not staged\n")
	write("untracked.txt", "untracked\n")
	indexBefore := git("write-tree")

	tree, err := TrackedTree(wt.Dir)
	if err != nil {
		t.Fatal(err)
	}
	names := git("ls-tree", "--name-only", tree)
	if !strings.Contains(names, "staged.txt") || strings.Contains(names, "untracked.txt") {
		t.Fatalf("tracked tree lists %q: want the staged file and not the untracked one", names)
	}
	if got := git("cat-file", "-p", tree+":README.md"); got != "edited, not staged" {
		t.Fatalf("the unstaged edit is missing from the tracked tree: %q", got)
	}
	if after := git("write-tree"); after != indexBefore {
		t.Fatalf("the worktree's index changed: %s -> %s", indexBefore, after)
	}
	if unstaged := git("diff", "--name-only"); unstaged != "README.md" {
		t.Fatalf("the edit must still be unstaged in the worktree: %q", unstaged)
	}
}
