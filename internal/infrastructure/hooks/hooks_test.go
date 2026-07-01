package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestInstallAndInstalled(t *testing.T) {
	gitDir := t.TempDir()
	if err := Install(gitDir, domain.AllHooks); err != nil {
		t.Fatal(err)
	}

	for _, h := range domain.AllHooks {
		path := filepath.Join(gitDir, "hooks", string(h))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("hook %s not written: %v", h, err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("hook %s must be executable, mode=%v", h, info.Mode())
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "warden run "+string(h)) {
			t.Errorf("hook %s does not invoke warden run: %q", h, data)
		}
	}

	installed := Installed(gitDir)
	if !installed[domain.PreCommit] || !installed[domain.PrePush] {
		t.Errorf("Installed = %v, want both true", installed)
	}
}

func TestInstall_RefusesToClobberForeignHook(t *testing.T) {
	gitDir := t.TempDir()
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(gitDir, []domain.Hook{domain.PreCommit}); err == nil {
		t.Fatal("expected Install to refuse overwriting a non-managed hook")
	}
	// The user's hook must be intact.
	data, _ := os.ReadFile(foreign)
	if !strings.Contains(string(data), "echo mine") {
		t.Error("foreign hook was clobbered")
	}
}

func TestRemove(t *testing.T) {
	gitDir := t.TempDir()
	if err := Install(gitDir, []domain.Hook{domain.PrePush}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(gitDir, domain.PrePush); err != nil {
		t.Fatal(err)
	}
	if Installed(gitDir)[domain.PrePush] {
		t.Error("hook should be gone after Remove")
	}
	// Removing an absent hook is a no-op, not an error.
	if err := Remove(gitDir, domain.PrePush); err != nil {
		t.Errorf("removing absent hook should be a no-op, got %v", err)
	}
}

func TestRemove_LeavesForeignHook(t *testing.T) {
	gitDir := t.TempDir()
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Remove(gitDir, domain.PrePush); err == nil {
		t.Error("Remove should refuse to delete a non-managed hook")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("foreign hook must survive Remove")
	}
}
