package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializedDependenciesHaveIndependentContents(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	file := filepath.Join(src, "dependency.js")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materializeTree(src, dst); err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(dst, "dependency.js")
	a, _ := os.Stat(file)
	b, _ := os.Stat(copy)
	if os.SameFile(a, b) {
		t.Fatal("dependency shares writable inode")
	}
	if err := os.WriteFile(copy, []byte("changed by step"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "original" {
		t.Fatalf("live install modified: %q %v", data, err)
	}
}
