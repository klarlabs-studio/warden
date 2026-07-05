package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// newTestRepo creates a temp git repo with an initial commit and returns its
// root. It skips the whole test when git is unavailable so the suite stays
// green on machines without a git CLI.
func newTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "warden@test.local")
	run("config", "user.name", "Warden Test")
	// commit.gpgsign off keeps CI machines with global signing config from
	// blocking commits.
	run("config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial commit")
	return dir
}

func TestOpenAndInspect(t *testing.T) {
	dir := newTestRepo(t)

	// Open from a subdirectory to prove it resolves the toplevel.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(sub)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	branch, err := repo.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("CurrentBranch = %q, want main", branch)
	}

	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("HeadSHA = %q, want 40-char sha", sha)
	}
}

func TestOpenNonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open on non-repo: want error, got nil")
	}
}

func TestDiffStatsStaged(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	// Stage a new file so there is a diff against the index.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	stats, err := repo.DiffStats("")
	if err != nil {
		t.Fatalf("DiffStats: %v", err)
	}
	if stats.FilesTouched != 1 {
		t.Errorf("FilesTouched = %d, want 1", stats.FilesTouched)
	}
	if stats.LinesChanged != 3 {
		t.Errorf("LinesChanged = %d, want 3", stats.LinesChanged)
	}
	if len(stats.Paths) != 1 || stats.Paths[0] != "a.txt" {
		t.Errorf("Paths = %v, want [a.txt]", stats.Paths)
	}

	staged, err := repo.StagedPaths()
	if err != nil {
		t.Fatalf("StagedPaths: %v", err)
	}
	if len(staged.Paths) != 1 || staged.Paths[0] != "a.txt" {
		t.Errorf("StagedPaths = %v, want [a.txt]", staged.Paths)
	}
}

func TestWorktreeFromHead(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	// Stage a change so the worktree is seeded with it.
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seeded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "seed.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatalf("CreateWorktreeFromHead: %v", err)
	}
	// The staged file must be present in the isolated worktree.
	if _, err := os.Stat(filepath.Join(wt.Dir, "seed.txt")); err != nil {
		t.Errorf("seeded file missing in worktree: %v", err)
	}

	if err := wt.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after Remove: %v", err)
	}
}

func TestNoteRoundTrip(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	// Absent note reads as (nil, nil).
	if rec, err := repo.ReadNote(sha); err != nil || rec != nil {
		t.Fatalf("ReadNote (absent) = (%v, %v), want (nil, nil)", rec, err)
	}

	want := domain.RunRecord{
		RunID:             "run-123",
		Timestamp:         "2026-07-01T00:00:00Z",
		WardenVersion:     "0.1.0",
		Agent:             map[domain.StepName]string{domain.StepReview: "claude"},
		StepsRun:          []domain.StepName{domain.StepReview, domain.StepTest},
		MatchedRules:      []string{"rule-a"},
		EvidenceChainRoot: "abc123",
		Evidence: []domain.EvidenceEntry{
			{Kind: "review", Source: "claude", Hash: "h1"},
		},
	}
	if err := repo.WriteNote(sha, want); err != nil {
		t.Fatalf("WriteNote: %v", err)
	}

	got, err := repo.ReadNote(sha)
	if err != nil {
		t.Fatalf("ReadNote: %v", err)
	}
	if got == nil {
		t.Fatal("ReadNote returned nil after WriteNote")
	}
	if got.RunID != want.RunID || got.WardenVersion != want.WardenVersion {
		t.Errorf("ReadNote = %+v, want %+v", got, want)
	}
	if got.Agent[domain.StepReview] != "claude" {
		t.Errorf("Agent = %v, want review->claude", got.Agent)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Hash != "h1" {
		t.Errorf("Evidence = %v, want one entry h1", got.Evidence)
	}
}

func TestCommitsSince(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	base, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	// Add two more commits after the adoption point.
	for _, name := range []string{"b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", dir, "add", name).CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
		if out, err := exec.Command("git", "-C", dir, "commit", "-q", "-m", "add "+name).CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v: %s", err, out)
		}
	}

	commits, err := repo.CommitsSince("HEAD", base)
	if err != nil {
		t.Fatalf("CommitsSince: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("CommitsSince returned %d commits, want 2", len(commits))
	}

	// Newest first: the first entry should be HEAD.
	head, _ := repo.HeadSHA()
	if commits[0] != head {
		t.Errorf("CommitsSince[0] = %s, want HEAD %s", commits[0], head)
	}

	author, date, subject, err := repo.CommitMeta(head)
	if err != nil {
		t.Fatalf("CommitMeta: %v", err)
	}
	if author != "Warden Test" {
		t.Errorf("author = %q, want Warden Test", author)
	}
	if subject != "add c.txt" {
		t.Errorf("subject = %q, want 'add c.txt'", subject)
	}
	if date == "" {
		t.Error("date is empty")
	}
}
