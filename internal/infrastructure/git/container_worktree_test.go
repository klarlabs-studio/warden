package git

import (
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestContainerWorktreesDoNotExposeHostDependencies(t *testing.T) {
	dir := newTestRepo(t)
	deps := filepath.Join(dir, "node_modules")
	if err := os.Mkdir(deps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(repo)
	for _, hook := range []domain.Hook{domain.PreCommit, domain.PrePush} {
		wt, err := adapter.SeedDependencyFreeWorktree(hook, "main")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = wt.Remove() }()
		if _, err := os.Lstat(filepath.Join(wt.Dir(), "node_modules")); !os.IsNotExist(err) {
			t.Fatal("host deps exposed", err)
		}
		clone, err := wt.Clone(true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = clone.Remove() }()
		if _, err := os.Lstat(filepath.Join(clone.Dir(), "node_modules")); !os.IsNotExist(err) {
			t.Fatal("clone exposed host deps", err)
		}
	}
	if repo.skipDependencyExposure {
		t.Fatal("shared repository mutated")
	}
}
