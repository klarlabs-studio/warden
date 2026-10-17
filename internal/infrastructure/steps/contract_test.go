package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestContractSnapshotObservesRepository(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	git("config", "user.name", "warden-contract")
	git("config", "user.email", "warden-contract")
	write("go.mod", "module example\n")
	write(".gitignore", "ignored\n")
	git("add", "go.mod", ".gitignore")
	git("commit", "-m", "initial")
	tree, inputs, err := contractSnapshot(context.Background(), dir, true)
	if err != nil || !domain.ValidTreeSHA(tree) || inputs["go.mod"] != domain.DigestBytes([]byte("module example\n")) {
		t.Fatalf("snapshot: %s %#v %v", tree, inputs, err)
	}
	write("ignored", "ambient")
	if _, _, err := contractSnapshot(context.Background(), dir, true); err == nil {
		t.Fatal("accepted ignored initial input")
	}
	if _, _, err := contractSnapshot(context.Background(), dir, false); err != nil {
		t.Fatal("rejected generated output", err)
	}
	git("update-index", "--assume-unchanged", "go.mod")
	if _, _, err := contractSnapshot(context.Background(), dir, false); err == nil {
		t.Fatal("accepted hidden index entry")
	}
	git("update-index", "--no-assume-unchanged", "go.mod")
	write("go.mod", "module changed\n")
	if _, _, err := contractSnapshot(context.Background(), dir, false); err == nil {
		t.Fatal("accepted modified tracked inputs")
	}
}
