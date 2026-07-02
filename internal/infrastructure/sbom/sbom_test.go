package sbom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollect_FindsLockfilesDigestedAndSorted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.sum", "example.com/x v1.0.0 h1:abc=\n")
	write(t, dir, "web/package-lock.json", `{"name":"x"}`)
	write(t, dir, "src/main.go", "package main") // not a lockfile
	write(t, dir, ".git/config", "[core]")       // .git skipped

	got := New().Collect(dir)
	if len(got) != 2 {
		t.Fatalf("expected 2 lockfiles, got %d: %+v", len(got), got)
	}
	// Sorted by path: go.sum before web/package-lock.json.
	if got[0].Path != "go.sum" || got[1].Path != "web/package-lock.json" {
		t.Errorf("not sorted by path: %+v", got)
	}
	if got[0].Ecosystem != "go" || got[1].Ecosystem != "npm" {
		t.Errorf("ecosystems wrong: %+v", got)
	}
	if !strings.HasPrefix(got[0].Digest, "sha256:") || len(got[0].Digest) != len("sha256:")+64 {
		t.Errorf("digest format wrong: %q", got[0].Digest)
	}
}

func TestCollect_DigestChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Cargo.lock", "v1")
	d1 := New().Collect(dir)[0].Digest
	write(t, dir, "Cargo.lock", "v2")
	d2 := New().Collect(dir)[0].Digest
	if d1 == d2 {
		t.Error("digest must change when the lockfile changes")
	}
}

func TestCollect_NoLockfilesIsEmpty(t *testing.T) {
	if got := New().Collect(t.TempDir()); len(got) != 0 {
		t.Errorf("expected empty SBOM, got %+v", got)
	}
}
