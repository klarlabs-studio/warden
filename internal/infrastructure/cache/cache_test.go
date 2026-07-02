package cache

import (
	"os"
	"path/filepath"
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

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/app/x.go", true},
		{"**/*.go", "README.md", false},
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false}, // single star doesn't cross a slash
		{"go.mod", "go.mod", true},
		{"src/**", "src/a/b.ts", true},
		{"src/**", "other/a.ts", false},
		{"cmd/?.go", "cmd/a.go", true},
		{"cmd/?.go", "cmd/ab.go", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.glob, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestFingerprint_ChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package a")
	write(t, dir, "b.txt", "ignored")
	c := Open(t.TempDir())

	fp1 := c.Fingerprint(dir, []string{"**/*.go"})
	if fp1 == "" {
		t.Fatal("expected a fingerprint for a matching file")
	}
	// A change to a non-matching file must NOT change the fingerprint.
	write(t, dir, "b.txt", "changed")
	if fp := c.Fingerprint(dir, []string{"**/*.go"}); fp != fp1 {
		t.Error("non-matching file changed the fingerprint")
	}
	// A change to a matching file MUST change it.
	write(t, dir, "a.go", "package a // edit")
	if fp := c.Fingerprint(dir, []string{"**/*.go"}); fp == fp1 {
		t.Error("matching file change did not bust the fingerprint")
	}
	// No match → empty (nothing to cache on).
	if fp := c.Fingerprint(dir, []string{"**/*.rs"}); fp != "" {
		t.Errorf("no-match fingerprint should be empty, got %q", fp)
	}
}

func TestFileCache_SeenRecordPersists(t *testing.T) {
	gitDir := t.TempDir()
	c := Open(gitDir)
	if c.Seen("k1") {
		t.Fatal("fresh cache must not report a key seen")
	}
	c.Record("k1")
	if !c.Seen("k1") {
		t.Error("recorded key must be seen")
	}
	// A fresh instance over the same dir reloads persisted keys.
	if !Open(gitDir).Seen("k1") {
		t.Error("recorded key must persist across Open")
	}
}

func TestOpen_CorruptFileIsEmptyNotError(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "warden-step-cache.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Open(gitDir).Seen("anything") {
		t.Error("a corrupt cache file must load empty, not crash or hit")
	}
}
