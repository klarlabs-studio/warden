package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A snapshot worktree holds exactly the snapshot's tree on top of its base:
// the live checkout's unstaged edit and untracked file are there (staged, as
// a pre-commit sees a change), its ignored files and host dependencies are
// not, and the live index is untouched.
func TestSnapshotWorktreeHoldsExactlyTheTree(t *testing.T) {
	dir := newTestRepo(t)
	put(t, filepath.Join(dir, ".gitignore"), "node_modules\n*.log\n")
	gitOut(t, dir, nil, "add", ".gitignore")
	gitOut(t, dir, nil, "commit", "-q", "-m", "ignore")
	base := gitOut(t, dir, nil, "rev-parse", "HEAD")

	put(t, filepath.Join(dir, "README.md"), "edited, not staged\n")
	put(t, filepath.Join(dir, "pkg", "new.go"), "package pkg\n")
	put(t, filepath.Join(dir, "debug.log"), "ignored\n")
	put(t, filepath.Join(dir, "node_modules", "dep", "index.js"), "host dependency\n")
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot, computed as the watcher does: a private index, add -A.
	private := filepath.Join(t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + private}
	gitOut(t, dir, env, "read-tree", "HEAD")
	gitOut(t, dir, env, "add", "-A")
	tree := gitOut(t, dir, env, "write-tree")

	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(repo)
	wt, err := adapter.SeedDependencyFreeSnapshot(base, tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wt.Remove() }()

	if got := gitOut(t, wt.Dir(), nil, "rev-parse", "HEAD"); got != base {
		t.Fatalf("worktree HEAD %s, base %s", got, base)
	}
	if got := gitOut(t, wt.Dir(), nil, "write-tree"); got != tree {
		t.Fatalf("worktree index writes %s, snapshot %s", got, tree)
	}
	if status := gitOut(t, wt.Dir(), nil, "status", "--porcelain"); status != "M  README.md\nA  pkg/new.go" {
		t.Fatalf("worktree status:\n%s", status)
	}
	if data, _ := os.ReadFile(filepath.Join(wt.Dir(), "README.md")); string(data) != "edited, not staged\n" {
		t.Fatalf("README.md in the worktree: %q", data)
	}
	for _, absent := range []string{"debug.log", "node_modules"} {
		if _, err := os.Lstat(filepath.Join(wt.Dir(), absent)); !os.IsNotExist(err) {
			t.Fatalf("%s present in the snapshot worktree: %v", absent, err)
		}
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("seeding the snapshot worktree rewrote the live index")
	}
	// A parallel step's clone carries the snapshot's untracked file too.
	clone, err := wt.Clone(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clone.Remove() }()
	if _, err := os.Stat(filepath.Join(clone.Dir(), "pkg", "new.go")); err != nil {
		t.Fatalf("clone lost the snapshot's new file: %v", err)
	}

	stats, err := adapter.SnapshotDiffStats(base, tree)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(stats.Paths)
	if stats.FilesTouched != 2 || !slices.Equal(stats.Paths, []string{"README.md", "pkg/new.go"}) {
		t.Fatalf("snapshot paths %+v", stats)
	}
}

func TestSnapshotWorktreeRejectsNonObjectNames(t *testing.T) {
	dir := newTestRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := gitOut(t, dir, nil, "rev-parse", "HEAD")
	tree := gitOut(t, dir, nil, "write-tree")
	adapter := NewAdapter(repo)
	for _, args := range [][2]string{{"HEAD", tree}, {base, "--exec=sh"}, {"", tree}} {
		if wt, err := adapter.SeedDependencyFreeSnapshot(args[0], args[1]); err == nil {
			_ = wt.Remove()
			t.Fatalf("seeded a snapshot worktree from %q", args)
		}
		if _, err := adapter.SnapshotDiffStats(args[0], args[1]); err == nil {
			t.Fatalf("diffed a snapshot from %q", args)
		}
	}
	// A tree the store does not hold fails, and leaves no worktree behind.
	missing := strings.Repeat("1", len(tree))
	if wt, err := adapter.SeedDependencyFreeSnapshot(base, missing); err == nil {
		_ = wt.Remove()
		t.Fatal("seeded a snapshot worktree from a missing tree")
	}
	if list := gitOut(t, dir, nil, "worktree", "list", "--porcelain"); strings.Contains(list, wardenWorktreePrefix) {
		t.Fatalf("a failed seed left a worktree registered:\n%s", list)
	}
}
