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

	fp1 := Fingerprint(dir)
	if fp1 == "" {
		t.Fatal("fingerprint of a non-empty tree must not be empty")
	}
	if Fingerprint(dir) != fp1 {
		t.Error("fingerprint must be stable when nothing changes")
	}

	// Edit the file with a distinct mtime → fingerprint changes.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Fingerprint(dir) == fp1 {
		t.Error("editing a file must change the fingerprint")
	}
}

func TestFingerprint_AddAndRemoveChangeIt(t *testing.T) {
	dir := t.TempDir()
	base := Fingerprint(dir)
	f := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	added := Fingerprint(dir)
	if added == base {
		t.Error("adding a file must change the fingerprint")
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if Fingerprint(dir) != base {
		t.Error("removing the added file must restore the fingerprint")
	}
}

func TestFingerprint_SkipsNoiseDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := Fingerprint(dir)

	// A change inside .git / node_modules must NOT affect the fingerprint.
	for _, noise := range []string{".git", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(dir, noise), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, noise, "junk"), []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if Fingerprint(dir) != before {
		t.Error("changes under skipped dirs must not change the fingerprint")
	}
}
