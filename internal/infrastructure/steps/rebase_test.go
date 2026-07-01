package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// newGitRepo initializes a temp git repo with one commit and returns its dir.
// It skips the test when git is unavailable so the suite stays green without a
// git CLI.
func newGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "warden@test.local")
	runGit(t, dir, "config", "user.name", "Warden Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestRebaseStepNoUpstream(t *testing.T) {
	dir := newGitRepo(t)
	res, err := NewRebaseStep().Run(context.Background(), application.StepContext{WorktreeDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass", res.Status)
	}
	if !strings.Contains(res.Summary, "no upstream") {
		t.Errorf("summary = %q, want it to mention no upstream", res.Summary)
	}
}

func TestRebaseStepCleanRebase(t *testing.T) {
	dir := newGitRepo(t)

	// Build an upstream that has advanced with a non-conflicting commit, and a
	// feature branch tracking it that adds a different file. Rebasing the feature
	// branch onto the advanced upstream should apply cleanly.
	runGit(t, dir, "branch", "upstream", "main")
	runGit(t, dir, "checkout", "-q", "-b", "feature", "main")
	// Configure feature's upstream to the local upstream branch.
	runGit(t, dir, "branch", "--set-upstream-to=upstream", "feature")

	// Advance upstream with its own file.
	runGit(t, dir, "checkout", "-q", "upstream")
	if err := os.WriteFile(filepath.Join(dir, "up.txt"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "up.txt")
	runGit(t, dir, "commit", "-q", "-m", "upstream advance")

	// Add a distinct commit on feature.
	runGit(t, dir, "checkout", "-q", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feat.txt"), []byte("feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "feat.txt")
	runGit(t, dir, "commit", "-q", "-m", "feature work")

	res, err := NewRebaseStep().Run(context.Background(), application.StepContext{WorktreeDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s, want pass; summary=%q findings=%+v", res.Status, res.Summary, res.Findings)
	}
	if !strings.Contains(res.Summary, "rebased onto") {
		t.Errorf("summary = %q, want it to mention rebased onto", res.Summary)
	}
	// After the rebase the upstream file must be present in the worktree.
	if _, err := os.Stat(filepath.Join(dir, "up.txt")); err != nil {
		t.Errorf("upstream file missing after rebase: %v", err)
	}
}

func TestRebaseStepName(t *testing.T) {
	if NewRebaseStep().Name() != domain.StepRebase {
		t.Errorf("Name() = %s, want rebase", NewRebaseStep().Name())
	}
}
