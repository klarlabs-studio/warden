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

// repoWithRemote builds a work repo whose `origin` is a bare repo carrying
// `main`, i.e. the shape every real branch has.
func repoWithRemote(t *testing.T) (work string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := t.TempDir()
	work = t.TempDir()
	gitCapture(t, bare, "init", "-q", "--bare", "-b", "main")
	gitCapture(t, work, "init", "-q", "-b", "main")
	gitCapture(t, work, "config", "user.email", "t@t.co")
	gitCapture(t, work, "config", "user.name", "t")
	writeFileAt(t, filepath.Join(work, "base.txt"), "base\n")
	gitCapture(t, work, "add", ".")
	gitCapture(t, work, "commit", "-q", "-m", "base")
	gitCapture(t, work, "remote", "add", "origin", bare)
	gitCapture(t, work, "push", "-q", "-u", "origin", "main")
	// origin/HEAD is what a real clone has; `git remote add` does not set it.
	gitCapture(t, work, "remote", "set-head", "origin", "main")
	return work
}

// gitCapture runs git and returns its output, unlike runGit in rebase_test.go
// which only asserts success.
func gitCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The #102 reproduction. A feature branch is pushed, main moves, and the
// developer rebases locally — the standard way to satisfy "head branch is not
// up to date with the base branch". `origin/feature` now holds the commit that
// was just replaced, so rebasing onto it would replay MAIN's commits onto a
// superseded tip and fail on main's own conflicts.
func TestRebaseStep_LocallyRebasedBranchStillRebases(t *testing.T) {
	work := repoWithRemote(t)

	// A feature branch, pushed.
	gitCapture(t, work, "checkout", "-q", "-b", "feature")
	writeFileAt(t, filepath.Join(work, "feature.txt"), "feature\n")
	gitCapture(t, work, "add", ".")
	gitCapture(t, work, "commit", "-q", "-m", "feature work")
	gitCapture(t, work, "push", "-q", "-u", "origin", "feature")

	// main moves, touching a file the feature branch also touches, so replaying
	// it onto the stale tip would genuinely conflict.
	gitCapture(t, work, "checkout", "-q", "main")
	writeFileAt(t, filepath.Join(work, "base.txt"), "base changed on main\n")
	gitCapture(t, work, "commit", "-q", "-am", "main moves")
	gitCapture(t, work, "push", "-q", "origin", "main")

	// The developer rebases the feature branch locally onto the updated main.
	gitCapture(t, work, "checkout", "-q", "feature")
	gitCapture(t, work, "rebase", "main")
	rebased := gitCapture(t, work, "rev-parse", "HEAD")

	// origin/feature is now stale: it still points at the pre-rebase commit.
	if stale := gitCapture(t, work, "rev-parse", "origin/feature"); stale == rebased {
		t.Fatal("test setup: origin/feature should be stale after the local rebase")
	}

	res, err := NewRebaseStep().Run(context.Background(), application.StepContext{
		WorktreeDir: work, Branch: "feature", Remote: "origin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%s) — a correctly rebased branch must not be refused:\n%+v",
			res.Status, res.Summary, res.Findings)
	}
	if !strings.Contains(res.Summary, "origin/main") {
		t.Errorf("summary = %q, want the integration base, not the branch's own ref", res.Summary)
	}
	// Already current with main, so the rebase changed nothing.
	if now := gitCapture(t, work, "rev-parse", "HEAD"); now != rebased {
		t.Errorf("HEAD moved from %s to %s; rebasing onto main should be a no-op here", rebased, now)
	}
}

// The configured PR base wins over the remote's default head — a repo whose
// PRs target `develop` integrates there, not into main.
func TestRebaseStep_PRBaseWinsOverDefaultHead(t *testing.T) {
	work := repoWithRemote(t)
	gitCapture(t, work, "checkout", "-q", "-b", "develop")
	gitCapture(t, work, "commit", "-q", "--allow-empty", "-m", "develop base")
	gitCapture(t, work, "push", "-q", "-u", "origin", "develop")

	gitCapture(t, work, "checkout", "-q", "-b", "feature")
	gitCapture(t, work, "commit", "-q", "--allow-empty", "-m", "feature work")

	res, err := NewRebaseStep().Run(context.Background(), application.StepContext{
		WorktreeDir: work, Branch: "feature", Remote: "origin", PRBase: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%s)", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "origin/develop") {
		t.Errorf("summary = %q, want origin/develop", res.Summary)
	}
}

// A PR base that does not exist on the remote must fall through rather than
// fail the gate on a typo in config.
func TestRebaseStep_UnknownPRBaseFallsBack(t *testing.T) {
	work := repoWithRemote(t)
	gitCapture(t, work, "checkout", "-q", "-b", "feature")
	gitCapture(t, work, "commit", "-q", "--allow-empty", "-m", "feature work")

	res, err := NewRebaseStep().Run(context.Background(), application.StepContext{
		WorktreeDir: work, Branch: "feature", Remote: "origin", PRBase: "no-such-branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%s)", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "origin/main") {
		t.Errorf("summary = %q, want a fallback to the default head", res.Summary)
	}
}

// The branch's own remote ref must never be chosen, even when it is the only
// thing an upstream points at — that is precisely the #102 failure.
func TestResolveIntegrationBase_NeverThePushedBranchItself(t *testing.T) {
	work := repoWithRemote(t)
	gitCapture(t, work, "checkout", "-q", "-b", "solo")
	gitCapture(t, work, "commit", "-q", "--allow-empty", "-m", "solo work")
	gitCapture(t, work, "push", "-q", "-u", "origin", "solo")

	// Point origin/HEAD at the branch itself, so every candidate resolves to it.
	gitCapture(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/solo")

	base, why := resolveIntegrationBase(context.Background(), application.StepContext{
		WorktreeDir: work, Branch: "solo", Remote: "origin",
	})
	if base != "" {
		t.Errorf("base = %q, want none — rebasing onto the branch's own ref is the bug", base)
	}
	if !strings.Contains(why, "no integration base") {
		t.Errorf("why = %q, want it to explain the skip", why)
	}
}

// A repo with no remote at all still passes rather than failing the gate.
func TestResolveIntegrationBase_NoRemote(t *testing.T) {
	dir := t.TempDir()
	gitCapture(t, dir, "init", "-q", "-b", "main")
	gitCapture(t, dir, "config", "user.email", "t@t.co")
	gitCapture(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCapture(t, dir, "add", ".")
	gitCapture(t, dir, "commit", "-q", "-m", "init")

	base, _ := resolveIntegrationBase(context.Background(), application.StepContext{
		WorktreeDir: dir, Branch: "main", Remote: "origin",
	})
	if base != "" {
		t.Errorf("base = %q, want none when there is no remote", base)
	}
}
