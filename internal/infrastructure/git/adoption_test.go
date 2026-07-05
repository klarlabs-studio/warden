package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitDir(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	gitDir, err := repo.GitDir()
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if !filepath.IsAbs(gitDir) {
		t.Errorf("GitDir = %q, want an absolute path", gitDir)
	}
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		t.Errorf("GitDir %q is not a directory: %v", gitDir, err)
	}
}

func TestAdoptionRoundTrip(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	// A repo Warden never initialized reports no adoption point.
	if got, err := repo.ReadAdoption(); err != nil || got != "" {
		t.Fatalf("ReadAdoption (absent) = (%q, %v), want (\"\", nil)", got, err)
	}

	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteAdoption(sha); err != nil {
		t.Fatalf("WriteAdoption: %v", err)
	}

	got, err := repo.ReadAdoption()
	if err != nil {
		t.Fatalf("ReadAdoption: %v", err)
	}
	if got != sha {
		t.Errorf("ReadAdoption = %q, want %q", got, sha)
	}

	// The record lives under .git so it is local, uncommitted state.
	gitDir, err := repo.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "warden", "adoption")); err != nil {
		t.Errorf("adoption file not under .git: %v", err)
	}

	// Overwriting records the newer adoption point.
	if err := repo.WriteAdoption("deadbeef"); err != nil {
		t.Fatalf("WriteAdoption (overwrite): %v", err)
	}
	if got, _ := repo.ReadAdoption(); got != "deadbeef" {
		t.Errorf("ReadAdoption after overwrite = %q, want deadbeef", got)
	}
}

func TestWorktreeFromBranchAndDiffSince(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	wt, err := repo.CreateWorktreeFromBranch("main", false)
	if err != nil {
		t.Fatalf("CreateWorktreeFromBranch: %v", err)
	}
	t.Cleanup(func() { _ = wt.Remove() })

	// A clean checkout of the branch tip: HEAD matches and there is no diff yet.
	head, err := wt.HeadSHA()
	if err != nil {
		t.Fatalf("worktree HeadSHA: %v", err)
	}
	if want := gitRev(t, dir, "main"); head != want {
		t.Errorf("worktree HeadSHA = %s, want %s", head, want)
	}

	if diff, err := wt.DiffSince(); err != nil || diff != "" {
		t.Fatalf("DiffSince (clean) = (%q, %v), want (\"\", nil)", diff, err)
	}

	// An unstaged edit shows up in DiffSince — the auto-fix delta the pre-commit
	// hook re-applies to the live tree.
	if err := os.WriteFile(filepath.Join(wt.Dir, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := wt.DiffSince()
	if err != nil {
		t.Fatalf("DiffSince: %v", err)
	}
	if diff == "" {
		t.Error("DiffSince returned empty after an edit, want a diff")
	}
}
