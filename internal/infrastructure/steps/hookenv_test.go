package steps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// hookEnv is what git exports to a pre-commit/pre-push hook process: the paths
// are RELATIVE to the repo the hook fired in. Inherited into a subprocess whose
// working directory is the disposable worktree — where `.git` is a gitfile, not
// a directory — `.git/index` resolves THROUGH that file and git dies with
// ENOTDIR ("index file open failed: Not a directory"). See #205.
func setHookEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_INDEX_FILE", ".git/index")
	t.Setenv("GIT_DIR", ".git")
	t.Setenv("GIT_PREFIX", "")
}

// linkedWorktree seeds a repo with an integration base and returns a detached
// linked worktree on the feature tip — the shape `warden run` actually validates
// in, and the shape that makes an inherited relative GIT_INDEX_FILE fatal.
func linkedWorktree(t *testing.T) (repo, worktree string) {
	t.Helper()
	repo = newGitRepo(t)

	// origin/main is the integration base; a real remote is unnecessary, the
	// step only ever resolves and rebases onto the ref.
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "feat.txt", "feat")
	commitAll(t, repo, "feature work")
	runGit(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "up.txt", "up")
	commitAll(t, repo, "base advance")
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "refs/heads/main")
	runGit(t, repo, "checkout", "-q", "feature")

	worktree = filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", "--detach", worktree, "feature")
	return repo, worktree
}

func TestRebaseStepIgnoresInheritedHookEnv(t *testing.T) {
	repo, worktree := linkedWorktree(t)
	_ = repo
	setHookEnv(t)

	sc := application.StepContext{WorktreeDir: worktree, Branch: "feature", Remote: "origin", PRBase: "main"}
	res, err := NewRebaseStep().Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s, want pass; summary=%q findings=%+v", res.Status, res.Summary, res.Findings)
	}
	// A skipped rebase also passes, so assert the rebase actually happened —
	// with the bug, base resolution fails first and the step skips silently.
	if !strings.Contains(res.Summary, "rebased onto origin/main") {
		t.Fatalf("summary = %q, want it to report rebasing onto origin/main", res.Summary)
	}
	if _, err := os.Stat(filepath.Join(worktree, "up.txt")); err != nil {
		t.Errorf("base commit's file missing after rebase: %v", err)
	}
}

func TestAgentStepIgnoresInheritedHookEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command assumes a POSIX shell")
	}
	setHookEnv(t)

	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Agent:       "test",
		// Fails loudly if the hook variables reached the agent's shell.
		AgentCommand: `test -z "$GIT_INDEX_FILE" && test -z "$GIT_DIR"`,
	}
	res, err := NewAgentStep(domain.StepReview, "review this").Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass; the agent shell inherited git's hook env: %+v", res.Status, res.Findings)
	}
}
