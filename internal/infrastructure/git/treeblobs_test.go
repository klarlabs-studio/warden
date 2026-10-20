package git

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ReadTreeBlobs answers from the commit's objects: files a step left on disk
// or rewrote after the commit are invisible, names with spaces and non-ASCII
// bytes arrive as written, and a symlink entry is not file content.
func TestReadTreeBlobs_ReadsCommittedRegularFilesOnly(t *testing.T) {
	dir := newTestRepo(t)
	writeRepoFile(t, dir, "a dir/naïve.lock", "committed\n")
	writeRepoFile(t, dir, "skip.txt", "filtered out\n")
	gitIn(t, dir, "add", "--", "a dir/naïve.lock", "skip.txt")
	hasLink := os.Symlink("naïve.lock", filepath.Join(dir, "a dir", "link.lock")) == nil
	if hasLink {
		gitIn(t, dir, "add", "--", "a dir/link.lock")
	}
	gitIn(t, dir, "commit", "-q", "-m", "tree")
	head := gitOutT(t, dir, "rev-parse", "HEAD")

	writeRepoFile(t, dir, "a dir/naïve.lock", "rewritten by a step\n")
	writeRepoFile(t, dir, "untracked.lock", "generated\n")

	keep := func(p string) bool { return filepath.Ext(p) == ".lock" }
	got, err := ReadTreeBlobs(dir, head, keep)
	if err != nil {
		t.Fatalf("ReadTreeBlobs: %v", err)
	}
	want := []TreeBlob{{Path: "a dir/naïve.lock", Content: []byte("committed\n")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadTreeBlobs = %+v, want %+v", got, want)
	}

	if _, err := ReadTreeBlobs(dir, "", keep); err == nil {
		t.Error("an empty revision must be an error, not the worktree")
	}
	if _, err := ReadTreeBlobs(dir, "no-such-ref", keep); err == nil {
		t.Error("an unresolvable revision must be an error")
	}
	if got, err := ReadTreeBlobs(dir, head, func(string) bool { return false }); err != nil || got != nil {
		t.Errorf("nothing kept = %+v, %v; want nil, nil", got, err)
	}
}
