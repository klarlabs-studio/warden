package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSameHookPathResolvesExistingAliases(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hooks")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if !SameHookPath(target, filepath.Join(target, ".")) || SameHookPath(target, filepath.Join(root, "missing")) {
		t.Fatal("path identity comparison failed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if !SameHookPath(target, alias) {
		t.Fatal("existing directory alias rejected")
	}
}
