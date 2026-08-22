package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// commitN adds a commit and returns its SHA.
func commitN(t *testing.T, dir, name string) string {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", name)
	run("commit", "-q", "-m", "add "+name)
	out := run("rev-parse", "HEAD")
	return trimNewline(out)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// The recorded adoption point is per-clone state in .git. Evidence has to be
// reproducible by someone who just cloned — so it is derived from the notes
// ref, which is shared, and it must land on the PARENT of the first noted
// commit so that commit is inside its own report.
func TestEarliestNotedAncestor_IsTheParentOfTheFirstGatedCommit(t *testing.T) {
	dir := newTestRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	first := commitN(t, dir, "a.txt")  // ungated
	second := commitN(t, dir, "b.txt") // the first gated one
	commitN(t, dir, "c.txt")

	if err := repo.WriteNote(second, domain.RunRecord{RunID: "r1", CommitSHA: second}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.EarliestNotedAncestor("main")
	if err != nil {
		t.Fatalf("EarliestNotedAncestor: %v", err)
	}
	if got != first {
		t.Errorf("adoption = %s, want the parent of the first noted commit (%s)", got, first)
	}
}

// Nothing gated means nothing to report, and saying so beats inventing a
// starting point that would make an ungated history look in-scope.
func TestEarliestNotedAncestor_EmptyWhenNothingIsNoted(t *testing.T) {
	dir := newTestRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	commitN(t, dir, "a.txt")

	got, err := repo.EarliestNotedAncestor("main")
	if err != nil {
		t.Fatalf("EarliestNotedAncestor: %v", err)
	}
	if got != "" {
		t.Errorf("derived %q from a repository with no notes", got)
	}
}

// A note on the root commit has no parent to fall back to; the root itself is
// the answer rather than an error.
func TestEarliestNotedAncestor_HandlesANotedRootCommit(t *testing.T) {
	dir := newTestRepo(t)
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteNote(root, domain.RunRecord{RunID: "r0", CommitSHA: root}); err != nil {
		t.Fatal(err)
	}
	commitN(t, dir, "a.txt")

	got, err := repo.EarliestNotedAncestor("main")
	if err != nil {
		t.Fatalf("EarliestNotedAncestor: %v", err)
	}
	if got != root {
		t.Errorf("adoption = %s, want the root commit %s", got, root)
	}
}
