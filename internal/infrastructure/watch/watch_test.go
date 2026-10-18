package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFingerprint_StableThenChangesOnEdit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}

	fp1 := NewTree(dir).Fingerprint()
	if fp1 == "" {
		t.Fatal("fingerprint of a non-empty tree must not be empty")
	}
	if NewTree(dir).Fingerprint() != fp1 {
		t.Error("fingerprint must be stable when nothing changes")
	}

	// Edit the file with a distinct mtime → fingerprint changes.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if NewTree(dir).Fingerprint() == fp1 {
		t.Error("editing a file must change the fingerprint")
	}
}

func TestFingerprint_AddAndRemoveChangeIt(t *testing.T) {
	dir := t.TempDir()
	base := NewTree(dir).Fingerprint()
	f := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	added := NewTree(dir).Fingerprint()
	if added == base {
		t.Error("adding a file must change the fingerprint")
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if NewTree(dir).Fingerprint() != base {
		t.Error("removing the added file must restore the fingerprint")
	}
}

func TestFingerprint_SkipsNoiseDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := NewTree(dir).Fingerprint()

	// A change inside .git / node_modules must NOT affect the fingerprint.
	for _, noise := range []string{".git", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(dir, noise), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, noise, "junk"), []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if NewTree(dir).Fingerprint() != before {
		t.Error("changes under skipped dirs must not change the fingerprint")
	}
}

func TestTree_ContentNotMtimeDrivesChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cover.out")
	if err := os.WriteFile(file, []byte("same bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := NewTree(dir)
	before := tree.Fingerprint()
	// A check rewriting a file with identical bytes must not retrigger itself.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if tree.Fingerprint() != before {
		t.Error("an mtime-only change must not change the fingerprint")
	}
	// Same size, same (future) mtime, different bytes: the racy case. The
	// file's mtime is still inside the racy window relative to its last hash,
	// so the tree re-reads it.
	if err := os.WriteFile(file, []byte("SAME BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if tree.Fingerprint() == before {
		t.Error("a same-size, same-mtime rewrite must change the fingerprint")
	}
}

func TestTree_SkipsNestedCheckouts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, ".claude", "worktrees", "agent")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := NewTree(dir)
	before := tree.Fingerprint()
	if err := os.WriteFile(filepath.Join(nested, "edit.go"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tree.Fingerprint() != before {
		t.Error("edits inside a nested linked worktree must not change the fingerprint")
	}
	// A plain subdirectory is still watched.
	if err := os.WriteFile(filepath.Join(dir, ".claude", "notes"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tree.Fingerprint() == before {
		t.Error("an ordinary subdirectory must still be watched")
	}
}
