package feedback

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// liveRepo builds a worktree exercising every class `git add -A` treats
// differently: a modified tracked file, a deleted one, an untracked file in a
// new directory, ignored files and directories, and an ignored file that was
// force-added (tracked, so add -A keeps updating it).
func liveRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	writeTestFile(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	writeTestFile(t, filepath.Join(dir, "kept"), "committed\n")
	writeTestFile(t, filepath.Join(dir, "deleted"), "committed\n")
	writeTestFile(t, filepath.Join(dir, "forced.log"), "committed\n")
	gitRun(t, dir, "add", ".gitignore", "kept", "deleted")
	gitRun(t, dir, "add", "-f", "forced.log")
	gitRun(t, dir, "commit", "-q", "-m", "files")

	writeTestFile(t, filepath.Join(dir, "kept"), "edited, not staged\n")
	if err := os.Remove(filepath.Join(dir, "deleted")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "sub", "new"), "untracked\n")
	writeTestFile(t, filepath.Join(dir, "debug.log"), "ignored\n")
	writeTestFile(t, filepath.Join(dir, "build", "out"), "ignored\n")
	writeTestFile(t, filepath.Join(dir, "forced.log"), "edited, not staged\n")
	return dir
}

// addAllTree is the answer computed without warden: copy the worktree,
// run the real `git add -A` in the copy, and write its tree.
func addAllTree(t *testing.T, dir string) string {
	t.Helper()
	copyDir := filepath.Join(t.TempDir(), "copy")
	if out, err := exec.Command("cp", "-R", dir, copyDir).CombinedOutput(); err != nil {
		t.Fatalf("copy worktree: %v %s", err, out)
	}
	gitRun(t, copyDir, "add", "-A")
	return gitRun(t, copyDir, "write-tree")
}

func fileSum(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// looseObjects is the repository's loose object count, as git reports it.
func looseObjects(t *testing.T, dir string) int {
	t.Helper()
	for _, line := range strings.Split(gitRun(t, dir, "count-objects", "-v"), "\n") {
		if v, ok := strings.CutPrefix(line, "count: "); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("git count-objects reported no count")
	return 0
}

// The live snapshot is the tree `git add -A` would stage, computed without
// touching the real index: unstaged edits and untracked files are in it,
// ignored files are not, and a force-added ignored file stays tracked.
func TestLiveSnapshotIsWhatAddAllWouldStage(t *testing.T) {
	dir := liveRepo(t)
	index := filepath.Join(dir, ".git", "index")
	before := fileSum(t, index)
	want := addAllTree(t, dir)

	// A commit in progress holds index.lock; the snapshot must not need it.
	lock := index + ".lock"
	writeTestFile(t, lock, "")
	snap, err := CurrentSnapshot(context.Background(), filepath.Join(dir, "sub"), domain.FeedbackModeLive, testConfig, "", KeepObjects)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if snap.Tree != want {
		t.Fatalf("live tree %s, git add -A in a copy wrote %s", snap.Tree, want)
	}
	if fileSum(t, index) != before {
		t.Fatal("computing the live snapshot rewrote the index")
	}
	if snap.Base != gitRun(t, dir, "rev-parse", "HEAD") || snap.Branch != "main" {
		t.Fatalf("snapshot %+v", snap)
	}
	files := strings.Split(gitRun(t, dir, "ls-tree", "-r", "--name-only", snap.Tree), "\n")
	has := map[string]bool{}
	for _, f := range files {
		has[f] = true
	}
	for _, f := range []string{"kept", "sub/new", "forced.log", ".gitignore"} {
		if !has[f] {
			t.Errorf("%s missing from the live tree %v", f, files)
		}
	}
	for _, f := range []string{"deleted", "debug.log", "build/out"} {
		if has[f] {
			t.Errorf("%s must not be in the live tree %v", f, files)
		}
	}
	if got := gitRun(t, dir, "show", snap.Tree+":kept"); got != "edited, not staged" {
		t.Fatalf("kept in the live tree: %q", got)
	}
	// The staged snapshot of the same worktree is still the index's tree.
	staged, err := CurrentSnapshot(context.Background(), dir, domain.FeedbackModeStaged, testConfig, "", DiscardObjects)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Tree != gitRun(t, dir, "write-tree") || staged.Tree == snap.Tree {
		t.Fatalf("staged tree %s, live tree %s", staged.Tree, snap.Tree)
	}
}

// A reader's snapshot never writes to the repository's object store, and a
// watcher's writes only content the store does not already hold: computing an
// unchanged snapshot again writes nothing.
func TestSnapshotObjectGrowth(t *testing.T) {
	dir := liveRepo(t)
	ctx := context.Background()
	start := looseObjects(t, dir)

	for range 3 {
		snap, err := CurrentSnapshot(ctx, dir, domain.FeedbackModeLive, testConfig, "", DiscardObjects)
		if err != nil {
			t.Fatal(err)
		}
		if n := looseObjects(t, dir); n != start {
			t.Fatalf("a discarding snapshot wrote %d objects to the repository", n-start)
		}
		if exec.Command("git", "-C", dir, "cat-file", "-e", snap.Tree).Run() == nil {
			t.Fatal("the discarded snapshot's tree is in the repository's store")
		}
	}

	kept, err := CurrentSnapshot(ctx, dir, domain.FeedbackModeLive, testConfig, "", KeepObjects)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := looseObjects(t, dir)
	if afterFirst == start {
		t.Fatal("a kept snapshot with new content wrote no objects")
	}
	gitRun(t, dir, "cat-file", "-e", kept.Tree)
	for range 3 {
		again, err := CurrentSnapshot(ctx, dir, domain.FeedbackModeLive, testConfig, "", KeepObjects)
		if err != nil {
			t.Fatal(err)
		}
		if again.Tree != kept.Tree {
			t.Fatalf("unchanged worktree, tree %s then %s", kept.Tree, again.Tree)
		}
		if n := looseObjects(t, dir); n != afterFirst {
			t.Fatalf("recomputing an unchanged snapshot wrote %d objects", n-afterFirst)
		}
	}
	// A discarding reader agrees with the watcher on the tree.
	read, err := CurrentSnapshot(ctx, dir, domain.FeedbackModeLive, testConfig, "", DiscardObjects)
	if err != nil {
		t.Fatal(err)
	}
	if read.ID != kept.ID {
		t.Fatalf("reader snapshot %s, watcher snapshot %s", read.ID, kept.ID)
	}
}

func TestCurrentSnapshotRejectsAnUnknownMode(t *testing.T) {
	if _, err := CurrentSnapshot(context.Background(), newRepo(t), "replay", testConfig, "", DiscardObjects); err == nil {
		t.Fatal("an unknown mode produced a snapshot")
	}
}

// The private index keeps the real one's mtime, so git's racy-entry rule
// re-reads exactly the entries it would re-read against the real index.
func TestCopyIndexKeepsTheModificationTime(t *testing.T) {
	src := filepath.Join(t.TempDir(), "index")
	writeTestFile(t, src, "index bytes")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyIndex(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("copy mtime %v, index mtime %v", info.ModTime(), stamp)
	}
	if data, _ := os.ReadFile(dst); string(data) != "index bytes" {
		t.Fatalf("copy holds %q", data)
	}
}
