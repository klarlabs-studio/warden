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
	if err := Install(gitDir, domain.AllHooks, "0.6.0"); err != nil {
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
		raw, _ := os.ReadFile(path)
		data := string(raw)
		if !strings.Contains(data, "hook="+string(h)) || !strings.Contains(data, `run "$hook"`) {
			t.Errorf("hook %s does not invoke warden run: %q", h, data)
		}
		if !strings.Contains(data, "pinned: 0.6.0") {
			t.Errorf("hook %s not version-pinned: %q", h, data)
		}
		if !strings.Contains(data, "command -v warden") {
			t.Errorf("hook %s should prefer a warden on PATH: %q", h, data)
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

	if err := Install(gitDir, []domain.Hook{domain.PreCommit}, "0.6.0"); err == nil {
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
	if err := Install(gitDir, []domain.Hook{domain.PrePush}, "0.6.0"); err != nil {
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

// Install is all-or-nothing. It used to write hooks one at a time and refuse
// on reaching the foreign one, so a repository with its own pre-push hook was
// left with warden's pre-commit installed by a command that had failed — a
// gate armed by an init that reported it had not happened.
func TestInstall_AForeignHookLeavesNothingInstalled(t *testing.T) {
	gitDir := t.TempDir()
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The foreign hook is the LAST one Install would reach.
	foreign := filepath.Join(hooksDir, "pre-push")
	if err := os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(gitDir, []domain.Hook{domain.PreCommit, domain.PrePush}, "0.6.0"); err == nil {
		t.Fatal("expected Install to refuse when one of the hooks is not warden's")
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "pre-commit")); !os.IsNotExist(err) {
		t.Errorf("pre-commit was installed by an Install that refused (stat err = %v)", err)
	}
	if installed := Installed(gitDir); installed[domain.PreCommit] || installed[domain.PrePush] {
		t.Errorf("Installed = %v after a refused Install, want none", installed)
	}
	data, _ := os.ReadFile(foreign)
	if string(data) != "#!/bin/sh\necho mine\n" {
		t.Errorf("foreign hook was modified: %q", data)
	}
}

func TestCheck(t *testing.T) {
	both := []domain.Hook{domain.PreCommit, domain.PrePush}
	tests := []struct {
		name    string
		prepare func(t *testing.T, gitDir string)
		wantErr bool
	}{
		{"no hooks directory", func(*testing.T, string) {}, false},
		{"warden's own hooks", func(t *testing.T, gitDir string) {
			if err := Install(gitDir, both, "0.6.0"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a foreign hook warden was not asked to install", func(t *testing.T, gitDir string) {
			writeForeign(t, gitDir, "commit-msg")
		}, false},
		{"a foreign pre-commit", func(t *testing.T, gitDir string) {
			writeForeign(t, gitDir, "pre-commit")
		}, true},
		{"a foreign pre-push", func(t *testing.T, gitDir string) {
			writeForeign(t, gitDir, "pre-push")
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := t.TempDir()
			tt.prepare(t, gitDir)
			before := snapshotDir(t, filepath.Join(gitDir, "hooks"))
			err := Check(gitDir, both)
			if (err != nil) != tt.wantErr {
				t.Errorf("Check error = %v, wantErr %v", err, tt.wantErr)
			}
			if after := snapshotDir(t, filepath.Join(gitDir, "hooks")); after != before {
				t.Errorf("Check wrote to the hooks directory:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func writeForeign(t *testing.T, gitDir, name string) {
	t.Helper()
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// snapshotDir renders a directory's file names and contents, "" when absent.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		b.WriteString(e.Name() + "=" + string(data) + ";")
	}
	return b.String()
}
