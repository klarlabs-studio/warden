package git

import (
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// TestAdapter exercises the pass-through Adapter that bridges *Repo to the
// application.Git port, confirming each method delegates to the underlying repo.
func TestAdapter(t *testing.T) {
	dir := newTestRepo(t)
	a := NewAdapter(&Repo{Dir: dir})

	if a.Root() != dir {
		t.Errorf("Root() = %q, want %q", a.Root(), dir)
	}

	branch, err := a.CurrentBranch()
	if err != nil || branch != "main" {
		t.Errorf("CurrentBranch() = (%q, %v), want (main, nil)", branch, err)
	}

	sha, err := a.HeadSHA()
	if err != nil || len(sha) != 40 {
		t.Errorf("HeadSHA() = (%q, %v), want a 40-char sha", sha, err)
	}

	base, err := a.MergeBase("HEAD")
	if err != nil || base != sha {
		t.Errorf("MergeBase(HEAD) = (%q, %v), want (%s, nil)", base, err, sha)
	}

	// Stage a change so the staged-diff accessors have something to report.
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("x\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "s.txt")

	if stats, err := a.StagedDiffStats(); err != nil || stats.FilesTouched != 1 {
		t.Errorf("StagedDiffStats() = (%+v, %v), want 1 file", stats, err)
	}
	if stats, err := a.DiffStats(""); err != nil || stats.FilesTouched != 1 {
		t.Errorf("DiffStats(\"\") = (%+v, %v), want 1 file", stats, err)
	}
}

func TestAdapterWorktreeAndPublish(t *testing.T) {
	dir := newTestRepo(t)
	a := NewAdapter(&Repo{Dir: dir})
	bare := setupBareRemote(t, dir)

	// Seed a worktree from HEAD through the adapter and drive its port methods.
	wt, err := a.SeedWorktreeFromHead()
	if err != nil {
		t.Fatalf("SeedWorktreeFromHead: %v", err)
	}
	if wt.Dir() == "" {
		t.Error("worktree Dir() is empty")
	}
	if _, err := wt.HeadSHA(); err != nil {
		t.Errorf("worktree HeadSHA: %v", err)
	}
	if _, err := wt.DiffSince(); err != nil {
		t.Errorf("worktree DiffSince: %v", err)
	}
	if err := wt.Remove(); err != nil {
		t.Errorf("worktree Remove: %v", err)
	}

	wtBranch, err := a.SeedWorktreeFromBranch("main")
	if err != nil {
		t.Fatalf("SeedWorktreeFromBranch: %v", err)
	}
	t.Cleanup(func() { _ = wtBranch.Remove() })

	// FastForwardTo through the adapter: main already points at expectedTip, so
	// a same-sha fast-forward is a no-op success.
	sha, err := a.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.FastForwardTo("main", sha, sha); err != nil {
		t.Errorf("FastForwardTo: %v", err)
	}

	// Push a branch, then write + push a note, all through the port.
	gitRun(t, dir, "branch", "feature", "main")
	if err := a.Push("origin", "feature"); err != nil {
		t.Errorf("Push: %v", err)
	}
	if err := a.WriteNote(sha, domain.RunRecord{RunID: "adapter-run"}); err != nil {
		t.Errorf("WriteNote: %v", err)
	}
	if err := a.PushNotes("origin"); err != nil {
		t.Errorf("PushNotes: %v", err)
	}
	if got := gitRev(t, bare, NotesRef); got == "" {
		t.Error("notes ref missing on remote after adapter PushNotes")
	}
}
