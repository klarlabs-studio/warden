package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func hookPathGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestHookPathConflictStopsInitBeforeArtifacts(t *testing.T) {
	dir := initRepo(t)
	hookPathGit(t, dir, "config", "core.hooksPath", ".husky/_")
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err == nil || !strings.Contains(err.Error(), "core.hooksPath") {
		t.Fatalf("inactive initialization accepted: %v", err)
	}
	if err := svc.SetHook(domain.PreCommit, true); err == nil {
		t.Fatal("enable claimed success for an unreachable hook")
	}
	for _, path := range []string{".warden.yaml", ".git/hooks/pre-commit", ".git/hooks/pre-push"} {
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Fatalf("failed initialization wrote %s: %v", path, err)
		}
	}
	if adoption, err := svc.Repo().ReadAdoption(); err != nil || adoption != "" {
		t.Fatalf("inactive gate created adoption point: %q %v", adoption, err)
	}
	if got := hookPathGit(t, dir, "config", "--get", "core.hooksPath"); got != ".husky/_" {
		t.Fatalf("existing hook ownership changed: %q", got)
	}
}

func TestHookPathOverrideHidesInstalledShims(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".husky/_", filepath.Join(t.TempDir(), "hooks with spaces")} {
		hookPathGit(t, dir, "config", "core.hooksPath", path)
		installed, err := svc.InstalledHooks()
		if err != nil || installed[domain.PreCommit] || installed[domain.PrePush] {
			t.Fatalf("inactive shims counted as armed: %v %v", installed, err)
		}
		if conflict, err := svc.HookPathConflict(); err != nil || conflict == "" {
			t.Fatalf("missing path diagnostic: %q %v", conflict, err)
		}
		if err := svc.RepinHook(domain.PreCommit); err == nil {
			t.Fatal("repin accepted an inactive directory")
		}
	}
	// Explicitly selecting the normal directory is compatible, not a conflict
	// just because core.hooksPath is present.
	hookPathGit(t, dir, "config", "core.hooksPath", ".git/hooks")
	if installed, err := svc.InstalledHooks(); err != nil || !installed[domain.PreCommit] {
		t.Fatalf("explicit default path rejected: %v %v", installed, err)
	}
	hookPathGit(t, dir, "config", "core.hooksPath", "")
	if _, err := svc.InstalledHooks(); err == nil {
		t.Fatal("invalid effective path must surface Git's error")
	}
}

func TestHookPathGlobalConfigAndLinkedWorktree(t *testing.T) {
	dir := initRepo(t)
	global := filepath.Join(t.TempDir(), "global-config")
	if err := os.WriteFile(global, []byte("[core]\n hooksPath = .husky/_\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err == nil {
		t.Fatal("global override ignored")
	}
	// The local selection overrides inherited config. A linked worktree shares
	// the common hooks directory, not its private index/HEAD directory.
	commonHooks := filepath.Join(dir, ".git", "hooks")
	hookPathGit(t, dir, "config", "core.hooksPath", commonHooks)
	linked := filepath.Join(t.TempDir(), "linked tree")
	hookPathGit(t, dir, "worktree", "add", "--detach", linked, "HEAD")
	t.Cleanup(func() { hookPathGit(t, dir, "worktree", "remove", "--force", linked) })
	svc, err = New(linked, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(commonHooks, "pre-commit")); err != nil {
		t.Fatalf("shared hook artifact missing: %v", err)
	}
	private := hookPathGit(t, linked, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(private, "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("unreachable worktree-private shim created: %v", err)
	}
	if installed, err := svc.InstalledHooks(); err != nil || !installed[domain.PreCommit] {
		t.Fatalf("shared hook not active: %v %v", installed, err)
	}
	if err := svc.SetHook(domain.PreCommit, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(commonHooks, "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("disable did not remove shared hook: %v", err)
	}
	hookPathGit(t, dir, "config", "extensions.worktreeConfig", "true")
	hookPathGit(t, linked, "config", "--worktree", "core.hooksPath", ".husky/_")
	if installed, err := svc.InstalledHooks(); err != nil || installed[domain.PrePush] {
		t.Fatalf("worktree-specific override ignored: %v %v", installed, err)
	}
	mainService, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if installed, err := mainService.InstalledHooks(); err != nil || !installed[domain.PrePush] {
		t.Fatalf("linked override incorrectly disabled the main checkout: %v %v", installed, err)
	}
}
