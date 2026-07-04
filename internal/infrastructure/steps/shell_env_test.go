package steps

import (
	"os"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// TestStepEnv_ScrubsGitHookVars guards the gate-vs-commit bug: git exports
// GIT_INDEX_FILE/GIT_DIR when running the pre-commit hook, and if a step
// inherits them, a git-aware tool inside the worktree (golangci-lint
// new-from-rev) resolves against the hook index instead of the worktree and
// mis-reports. stepEnv must strip them.
func TestStepEnv_ScrubsGitHookVars(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "/live/.git/index")
	t.Setenv("GIT_DIR", "/live/.git")
	t.Setenv("KEEP_ME", "yes")

	env := stepEnv(application.StepContext{Hook: domain.PreCommit})

	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_INDEX_FILE=") || strings.HasPrefix(kv, "GIT_DIR=") {
			t.Fatalf("stepEnv leaked git hook var: %q", kv)
		}
	}
	var kept bool
	for _, kv := range env {
		if kv == "KEEP_ME=yes" {
			kept = true
		}
	}
	if !kept {
		t.Error("stepEnv dropped a non-hook env var")
	}
}

// TestStepEnv_PerWorktreeGolangciCache guards the stale-cache bug: golangci-lint
// caches results keyed to the worktree's absolute path, so a shared cache across
// fresh random worktrees returns dead-path results. Each run must get its own.
func TestStepEnv_PerWorktreeGolangciCache(t *testing.T) {
	env := stepEnv(application.StepContext{Hook: domain.PreCommit, WorktreeDir: "/tmp/warden-wt-x"})
	got := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOLANGCI_LINT_CACHE=") {
			got = kv
		}
	}
	if got != "GOLANGCI_LINT_CACHE=/tmp/warden-wt-x-golangci-cache" {
		t.Fatalf("golangci cache = %q", got)
	}
	// With no worktree, stepEnv must not *introduce* a cache override — compare
	// counts against the ambient env, since running inside warden's own gate
	// already exports GOLANGCI_LINT_CACHE.
	count := func(env []string) int {
		n := 0
		for _, kv := range env {
			if strings.HasPrefix(kv, "GOLANGCI_LINT_CACHE=") {
				n++
			}
		}
		return n
	}
	if got, want := count(stepEnv(application.StepContext{Hook: domain.PreCommit})), count(os.Environ()); got != want {
		t.Errorf("no worktree changed GOLANGCI_LINT_CACHE count: %d, want %d", got, want)
	}
}
