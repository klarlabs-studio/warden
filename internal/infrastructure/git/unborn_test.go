package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// newEmptyRepo creates a repository with no commit: the state `git init`
// leaves behind, and the one a first commit is made from.
func newEmptyRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "warden-test")
	run("config", "user.name", "Warden Test")
	run("config", "commit.gpgsign", "false")
	return dir, run
}

// A branch with no commit has a name and nothing else. Everything that asks
// "which branch is this" before the first commit used to get git's
// "ambiguous argument 'HEAD'" instead of an answer, so the first commit in a
// repository could not be gated at all.
func TestUnbornBranchIsNamedAndRecognized(t *testing.T) {
	dir, run := newEmptyRepo(t)
	repo := &Repo{Dir: dir}

	branch, err := repo.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch on an unborn branch: %v", err)
	}
	if branch != "main" {
		t.Errorf("CurrentBranch = %q, want main", branch)
	}
	if unborn, err := repo.Unborn(); err != nil || !unborn {
		t.Errorf("Unborn = %v, %v; want true", unborn, err)
	}
	if has, err := repo.HasCommits(); err != nil || has {
		t.Errorf("HasCommits = %v, %v; want false in an empty repository", has, err)
	}

	run("commit", "-q", "--allow-empty", "-m", "first")
	if unborn, err := repo.Unborn(); err != nil || unborn {
		t.Errorf("Unborn = %v, %v after the first commit; want false", unborn, err)
	}
	if has, err := repo.HasCommits(); err != nil || !has {
		t.Errorf("HasCommits = %v, %v after the first commit; want true", has, err)
	}

	// An orphan branch is unborn too, in a repository that is not empty. The
	// two must stay distinguishable: only the empty one has nothing to predate.
	run("switch", "-q", "--orphan", "fresh")
	if unborn, err := repo.Unborn(); err != nil || !unborn {
		t.Errorf("Unborn = %v, %v on an orphan branch; want true", unborn, err)
	}
	if has, err := repo.HasCommits(); err != nil || !has {
		t.Errorf("HasCommits = %v, %v on an orphan branch; want true", has, err)
	}
	if branch, err := repo.CurrentBranch(); err != nil || branch != "fresh" {
		t.Errorf("CurrentBranch = %q, %v on an orphan branch; want fresh", branch, err)
	}
}

// A detached HEAD is not an unborn branch, and must keep answering "HEAD".
func TestDetachedHeadIsNotUnborn(t *testing.T) {
	dir := newTestRepo(t)
	if out, err := exec.Command("git", "-C", dir, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach: %v: %s", err, out)
	}
	repo := &Repo{Dir: dir}
	if unborn, err := repo.Unborn(); err != nil || unborn {
		t.Errorf("Unborn = %v, %v on a detached HEAD; want false", unborn, err)
	}
	if branch, err := repo.CurrentBranch(); err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch = %q, %v on a detached HEAD; want HEAD", branch, err)
	}
}

// The pre-commit worktree for a first commit must hold exactly what is
// staged: nothing less (the gate would pass a tree it never saw) and nothing
// more (an unstaged file would be checked as if it were being committed).
func TestWorktreeForAFirstCommitHoldsExactlyTheStagedTree(t *testing.T) {
	dir, run := newEmptyRepo(t)
	write := func(name, content string) {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	write("pkg/util.go", "package pkg\n")
	write("blob.bin", "\x00\x01\x02binary\x00")
	write("unstaged.txt", "not part of the commit\n")
	run("add", "main.go", "pkg/util.go", "blob.bin")
	// Staged, then edited again: the index version is what gets committed.
	write("main.go", "package main // edited after staging\n")

	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatalf("CreateWorktreeFromHead on an unborn branch: %v", err)
	}
	defer func() { _ = wt.Remove() }()

	var got []string
	err = filepath.WalkDir(wt.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(wt.Dir, path)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"blob.bin", "main.go", "pkg/util.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("worktree holds %v, want exactly the staged files %v", got, want)
	}
	if data, _ := os.ReadFile(filepath.Join(wt.Dir, "main.go")); string(data) != "package main\n" {
		t.Errorf("main.go = %q, want the staged content, not the later edit", data)
	}
	if data, _ := os.ReadFile(filepath.Join(wt.Dir, "blob.bin")); string(data) != "\x00\x01\x02binary\x00" {
		t.Errorf("staged binary did not survive seeding: %q", data)
	}

	// The stand-in base must not become history: no ref may point at it, and
	// the branch must still be unborn so the user's commit is a root commit.
	if refs := run("for-each-ref"); refs != "" {
		t.Errorf("gating a first commit created refs: %s", refs)
	}
	if unborn, err := repo.Unborn(); err != nil || !unborn {
		t.Errorf("Unborn = %v, %v after gating; the branch must still have no commit", unborn, err)
	}
}

// One stand-in object, however many times a first commit is attempted.
func TestEmptyBaseCommitIsDeterministic(t *testing.T) {
	dir, run := newEmptyRepo(t)
	repo := &Repo{Dir: dir}
	// A different identity and clock in the environment must not change it.
	t.Setenv("GIT_AUTHOR_NAME", "someone else")
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-01T00:00:00+0000")

	first, err := repo.emptyBaseCommit()
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.emptyBaseCommit()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("empty base differs between calls: %s vs %s", first, second)
	}
	empty, err := repo.EmptyTree()
	if err != nil {
		t.Fatal(err)
	}
	if tree := run("rev-parse", first+"^{tree}"); tree != empty {
		t.Errorf("empty base has tree %s, want the empty tree %s", tree, empty)
	}
	if parents := run("rev-list", "--parents", "-1", first); strings.Contains(parents, " ") {
		t.Errorf("empty base has a parent: %s", parents)
	}
}

// An adoption point is exclusive, which is right for "everything after the
// commit I adopted at" and wrong for a repository adopted before its first
// commit: there the first commit was made under the gate and has no
// predecessor to name. AdoptionRoot is that case, and it must include the
// root.
func TestCommitsSinceRootIncludesTheRootCommit(t *testing.T) {
	dir, run := newEmptyRepo(t)
	repo := &Repo{Dir: dir}
	run("commit", "-q", "--allow-empty", "-m", "root")
	root := run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "second")
	second := run("rev-parse", "HEAD")

	fromRoot, err := repo.CommitsSince("main", domain.AdoptionRoot)
	if err != nil {
		t.Fatalf("CommitsSince(root): %v", err)
	}
	if strings.Join(fromRoot, ",") != second+","+root {
		t.Errorf("CommitsSince(main, root) = %v, want [%s %s]", fromRoot, second, root)
	}

	// The ordinary point stays exclusive.
	fromSHA, err := repo.CommitsSince("main", root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fromSHA, ",") != second {
		t.Errorf("CommitsSince(main, <root sha>) = %v, want only %s", fromSHA, second)
	}
}
