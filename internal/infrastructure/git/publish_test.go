package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// gitRev runs `git rev-parse <ref>` in dir and returns the SHA, failing the
// test on error. It reads refs without going through the (unexported) run
// helper so setup stays independent of the code under test.
func gitRev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v: %s", ref, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitRun runs a git subcommand with its working directory set to dir.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestApplyPatch(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	t.Run("empty patch is a no-op", func(t *testing.T) {
		if err := repo.ApplyPatch("   \n"); err != nil {
			t.Fatalf("ApplyPatch(empty): %v", err)
		}
	})

	t.Run("applies a unified diff to the working tree", func(t *testing.T) {
		readme := filepath.Join(dir, "README.md")

		// Produce a genuine unified diff by making a change, capturing git diff,
		// then reverting — this guarantees the patch's index/context lines match.
		if err := os.WriteFile(readme, []byte("goodbye\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "-C", dir, "diff").CombinedOutput()
		if err != nil {
			t.Fatalf("git diff: %v: %s", err, out)
		}
		patch := string(out)
		gitRun(t, dir, "checkout", "--", "README.md")

		// Confirm the revert took, then apply the captured patch.
		if data, _ := os.ReadFile(readme); string(data) != "hello\n" {
			t.Fatalf("revert failed, README = %q", data)
		}
		if err := repo.ApplyPatch(patch); err != nil {
			t.Fatalf("ApplyPatch: %v", err)
		}
		if data, _ := os.ReadFile(readme); string(data) != "goodbye\n" {
			t.Errorf("after ApplyPatch README = %q, want %q", data, "goodbye\n")
		}
	})

	t.Run("a malformed patch is a reported error", func(t *testing.T) {
		if err := repo.ApplyPatch("this is not a diff at all"); err == nil {
			t.Fatal("ApplyPatch(garbage): want error, got nil")
		}
	})
}

func TestFastForwardTo(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	// A: the initial commit. Create branch "target" at A, then advance main to a
	// new commit B. target still points at A.
	shaA := gitRev(t, dir, "HEAD")
	gitRun(t, dir, "branch", "target", shaA)

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "commit B")
	shaB := gitRev(t, dir, "HEAD")

	t.Run("advances a branch when the tip is unchanged", func(t *testing.T) {
		if err := repo.FastForwardTo("target", shaB, shaA); err != nil {
			t.Fatalf("FastForwardTo: %v", err)
		}
		if got := gitRev(t, dir, "target"); got != shaB {
			t.Errorf("target = %s, want %s", got, shaB)
		}
	})

	t.Run("refuses when the tip moved", func(t *testing.T) {
		// target now points at B; claiming it still points at A must fail with
		// ErrBranchMoved rather than clobber the branch.
		err := repo.FastForwardTo("target", shaB, shaA)
		if !errors.Is(err, ErrBranchMoved) {
			t.Fatalf("FastForwardTo(stale tip) err = %v, want ErrBranchMoved", err)
		}
	})

	t.Run("propagates an error for an unknown branch", func(t *testing.T) {
		if err := repo.FastForwardTo("no-such-branch", shaB, shaA); err == nil {
			t.Fatal("FastForwardTo(unknown branch): want error, got nil")
		}
	})
}

// setupBareRemote creates a bare repo and wires it as remote "origin" on the
// work repo, pushing main so the commit exists on both sides.
func setupBareRemote(t *testing.T, workDir string) string {
	t.Helper()
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	gitRun(t, workDir, "remote", "add", "origin", bare)
	gitRun(t, workDir, "push", "-q", "origin", "main")
	return bare
}

func TestPushAndNotesRoundTrip(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)

	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	// Push a fresh branch so Push's own code path is exercised.
	gitRun(t, dir, "branch", "feature", "main")
	if err := repo.Push("origin", "feature"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := gitRev(t, bare, "refs/heads/feature"); got != sha {
		t.Errorf("remote feature = %s, want %s", got, sha)
	}

	// Write a note locally and push the notes ref to the bare remote.
	rec := domain.RunRecord{RunID: "run-xyz", WardenVersion: "0.1.0"}
	if err := repo.WriteNote(sha, rec); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}
	if err := repo.PushNotes("origin"); err != nil {
		t.Fatalf("PushNotes: %v", err)
	}
	if got := gitRev(t, bare, NotesRef); got == "" {
		t.Error("notes ref missing on remote after PushNotes")
	}

	// A second clone fetches the notes and reads the record back — the full
	// provenance round-trip across machines.
	parent := t.TempDir()
	cloneDir := filepath.Join(parent, "clone")
	gitRun(t, parent, "clone", "-q", bare, cloneDir)
	cloneRepo := &Repo{Dir: cloneDir}
	if err := cloneRepo.FetchNotes("origin"); err != nil {
		t.Fatalf("FetchNotes: %v", err)
	}
	got, err := cloneRepo.ReadNote(sha)
	if err != nil {
		t.Fatalf("ReadNote (clone): %v", err)
	}
	if got == nil || got.RunID != "run-xyz" {
		t.Errorf("clone ReadNote = %+v, want RunID run-xyz", got)
	}
}

func TestMergeBase(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	base := gitRev(t, dir, "HEAD")
	// A new commit on main; merge-base of HEAD and the original commit is that
	// original commit.
	if err := os.WriteFile(filepath.Join(dir, "m.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "m.txt")
	gitRun(t, dir, "commit", "-q", "-m", "advance")

	got, err := repo.MergeBase(base)
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if got != base {
		t.Errorf("MergeBase = %s, want %s", got, base)
	}
}
