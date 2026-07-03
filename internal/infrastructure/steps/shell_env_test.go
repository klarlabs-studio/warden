package steps

import (
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
