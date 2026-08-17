package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A worktree is torn down by a deferred Remove, and a deferred Remove does not
// run when the process is killed. The directory is under os.MkdirTemp so the OS
// eventually reaps it, but the registration under .git/worktrees has no janitor
// and survives indefinitely -- reported forever after by `git worktree list` as
// prunable, and pinning the detached HEAD it points at so `git gc` cannot
// release those objects.
//
// These tests fake that end state directly (register a worktree, delete its
// directory) because reproducing it honestly would mean killing a real warden
// mid-run.
func sweepTestRepo(t *testing.T) (*Repo, func(args ...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	// Not an address: git does not validate the format, and a literal email
	// here trips the secret scanner for no benefit.
	gitRun("config", "user.email", "warden-test")
	gitRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-m", "init")
	return &Repo{Dir: dir}, gitRun
}

// registrations counts the admin entries under .git/worktrees.
func registrations(t *testing.T, repo *Repo) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo.Dir, ".git", "worktrees"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// abandon registers a worktree at path and then deletes its directory, which is
// exactly the state a killed run leaves behind.
func abandon(t *testing.T, gitRun func(...string), path string) {
	t.Helper()
	gitRun("worktree", "add", "--detach", path, "HEAD")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func TestPruneStaleWardenWorktrees_RemovesAbandonedRegistrations(t *testing.T) {
	repo, gitRun := sweepTestRepo(t)

	tmp := t.TempDir()
	for _, name := range []string{"warden-wt-111", "warden-wt-222", "warden-wt-333"} {
		abandon(t, gitRun, filepath.Join(tmp, name))
	}
	if got := registrations(t, repo); got != 3 {
		t.Fatalf("setup: %d registrations, want 3", got)
	}

	repo.pruneStaleWardenWorktrees()

	if got := registrations(t, repo); got != 0 {
		t.Errorf("%d registrations survived the sweep, want 0", got)
	}
	out, err := repo.run("worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, wardenWorktreePrefix) {
		t.Errorf("`git worktree list` still reports warden worktrees:\n%s", out)
	}
}

// The sweep must not touch a worktree that is still on disk. Two warden runs can
// overlap -- parallel steps clone, and a developer can commit in one repo while
// CI pushes in another -- and removing a live registration would break the run
// that owns it.
func TestPruneStaleWardenWorktrees_KeepsLiveWorktrees(t *testing.T) {
	repo, gitRun := sweepTestRepo(t)

	tmp := t.TempDir()
	live := filepath.Join(tmp, "warden-wt-live")
	gitRun("worktree", "add", "--detach", live, "HEAD") // left on disk

	dead := filepath.Join(tmp, "warden-wt-dead")
	abandon(t, gitRun, dead)

	repo.pruneStaleWardenWorktrees()

	if got := registrations(t, repo); got != 1 {
		t.Fatalf("%d registrations, want 1 (the live one)", got)
	}
	out, err := repo.run("worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "warden-wt-live") {
		t.Errorf("the live worktree was swept:\n%s", out)
	}
	if strings.Contains(out, "warden-wt-dead") {
		t.Errorf("the dead worktree survived:\n%s", out)
	}
}

// Warden runs inside other people's repositories. Someone else's abandoned
// worktree is not warden's to collect -- it may be on an unmounted volume and
// merely absent rather than dead. This is why the sweep is not
// `git worktree prune`, which cannot tell the difference.
func TestPruneStaleWardenWorktrees_LeavesForeignWorktreesAlone(t *testing.T) {
	repo, gitRun := sweepTestRepo(t)

	tmp := t.TempDir()
	foreign := filepath.Join(tmp, "someone-elses-wt")
	abandon(t, gitRun, foreign)

	repo.pruneStaleWardenWorktrees()

	if got := registrations(t, repo); got != 1 {
		t.Fatalf("%d registrations, want 1: a foreign worktree must survive", got)
	}
	out, err := repo.run("worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "someone-elses-wt") {
		t.Errorf("swept a worktree warden did not create:\n%s", out)
	}
}

// The sweep runs on the path that creates a worktree, so a normal run collects
// what the previous killed one left. This is the behavior that actually fixes
// the leak; the unit tests above only prove the sweep itself.
func TestAddDetachedWorktree_SweepsPreviousLeftovers(t *testing.T) {
	repo, gitRun := sweepTestRepo(t)

	tmp := t.TempDir()
	abandon(t, gitRun, filepath.Join(tmp, "warden-wt-orphan"))
	if got := registrations(t, repo); got != 1 {
		t.Fatalf("setup: %d registrations, want 1", got)
	}

	wt, err := repo.addDetachedWorktree("HEAD", false)
	if err != nil {
		t.Fatalf("addDetachedWorktree: %v", err)
	}
	defer func() { _ = wt.Remove() }()

	// Only the one just created should remain.
	if got := registrations(t, repo); got != 1 {
		t.Errorf("%d registrations after a fresh run, want 1 (the new worktree)", got)
	}
	out, err := repo.run("worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "warden-wt-orphan") {
		t.Errorf("the orphan survived a subsequent run:\n%s", out)
	}
}

// A repo that has never had a linked worktree has no .git/worktrees at all. The
// sweep must treat that as nothing to do rather than an error, since it runs
// before every worktree creation.
func TestPruneStaleWardenWorktrees_NoWorktreesDirIsNotAnError(t *testing.T) {
	repo, _ := sweepTestRepo(t)
	repo.pruneStaleWardenWorktrees() // must not panic
	if got := registrations(t, repo); got != 0 {
		t.Errorf("%d registrations, want 0", got)
	}
}
