package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// exitedPID is the pid of a process that has already exited.
func exitedPID(t *testing.T) int {
	t.Helper()
	name, args := "sh", []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit 0"}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// ownerFile is the owner marker of the worktree at dir.
func ownerFile(t *testing.T, dir string) string {
	t.Helper()
	admin, err := worktreeAdminDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(admin, worktreeOwnerFile)
}

// A run killed outright (SIGKILL, a CI timeout) never runs its deferred
// Remove, and its temp directory stays on disk until the OS reaps it. While
// it does, the directory alone cannot tell a dead run's worktree from a
// concurrent live one. The owner marker can: a worktree whose recorded owner
// on this machine has exited is pruned, directory and registration both.
func TestPruneStaleWardenWorktrees_RemovesADeadOwnersWorktree(t *testing.T) {
	repo, _ := sweepTestRepo(t)
	wt, err := repo.addDetachedWorktree("HEAD", false)
	if err != nil {
		t.Fatal(err)
	}
	marker := ownerFile(t, wt.Dir)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the new worktree records no owner: %v", err)
	}
	host, _ := os.Hostname()
	if want := strconv.Itoa(os.Getpid()) + "\n" + host + "\n"; string(data) != want {
		t.Fatalf("owner marker %q, want %q", data, want)
	}

	// The owner is alive: the sweep keeps it.
	repo.pruneStaleWardenWorktrees()
	if _, err := os.Stat(wt.Dir); err != nil || registrations(t, repo) != 1 {
		t.Fatalf("a live owner's worktree was swept: %v", err)
	}

	// The owner has exited: the sweep removes it.
	if err := os.WriteFile(marker, []byte(strconv.Itoa(exitedPID(t))+"\n"+host+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt.Dir+"-golangci-cache", 0o700); err != nil {
		t.Fatal(err)
	}
	repo.pruneStaleWardenWorktrees()
	if got := registrations(t, repo); got != 0 {
		t.Fatalf("%d registrations survive a dead owner", got)
	}
	for _, dir := range []string{wt.Dir, wt.Dir + "-golangci-cache"} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s survives a dead owner: %v", dir, err)
		}
	}
	out, err := repo.run("worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, wardenWorktreePrefix) {
		t.Fatalf("git still lists the dead owner's worktree:\n%s", out)
	}
}

// Only an owner the sweep can actually judge is judged. A marker naming
// another machine (a repository on a shared volume) or one that cannot be
// read leaves the worktree alone, as does a worktree with no marker at all
// (created by an older warden, or between its creation and the marker).
func TestPruneStaleWardenWorktrees_KeepsOwnersItCannotJudge(t *testing.T) {
	repo, _ := sweepTestRepo(t)
	dead := strconv.Itoa(exitedPID(t))
	for name, content := range map[string]*string{
		"another machine": ptr(dead + "\nsome-other-host\n"),
		"unreadable":      ptr("not a pid\n"),
		"no host":         ptr(dead + "\n"),
		"no marker":       nil,
	} {
		wt, err := repo.addDetachedWorktree("HEAD", false)
		if err != nil {
			t.Fatal(err)
		}
		marker := ownerFile(t, wt.Dir)
		if content == nil {
			err = os.Remove(marker)
		} else {
			err = os.WriteFile(marker, []byte(*content), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		repo.pruneStaleWardenWorktrees()
		if _, err := os.Stat(wt.Dir); err != nil {
			t.Fatalf("%s: the worktree was swept: %v", name, err)
		}
		if err := wt.Remove(); err != nil {
			t.Fatal(err)
		}
	}
}

func ptr(s string) *string { return &s }
