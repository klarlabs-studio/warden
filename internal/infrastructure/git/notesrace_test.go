package git

import (
	"os/exec"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// noteFor builds a minimal record bound to sha.
func noteFor(sha, runID string) domain.RunRecord {
	return domain.RunRecord{
		RunID:             runID,
		CommitSHA:         sha,
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
	}
}

// twoWriters sets up a bare remote and two clones of it — a stand-in for a
// developer's machine and a CI runner, which since 0.22.0 both write
// refs/notes/warden (#186).
func twoWriters(t *testing.T) (bare string, a, b *Repo, shaA, shaB string) {
	t.Helper()
	bare = t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}

	seed := newTestRepo(t)
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "-u", "origin", "HEAD:refs/heads/main")

	clone := func() string {
		d := t.TempDir()
		if out, err := exec.Command("git", "clone", bare, d).CombinedOutput(); err != nil {
			t.Fatalf("clone: %v %s", err, out)
		}
		gitRun(t, d, "config", "user.email", "warden-notes-race")
		gitRun(t, d, "config", "user.name", "warden-notes-race")
		return d
	}
	dirA, dirB := clone(), clone()
	a, b = &Repo{Dir: dirA}, &Repo{Dir: dirB}

	// Each writer has its own commit to attest — the ordinary case: a developer
	// notes their commit while CI notes a different one.
	gitRun(t, dirA, "commit", "--allow-empty", "--no-verify", "-m", "from A")
	gitRun(t, dirB, "commit", "--allow-empty", "--no-verify", "-m", "from B")
	return bare, a, b, gitRev(t, dirA, "HEAD"), gitRev(t, dirB, "HEAD")
}

// The losing side of a notes race must still publish.
//
// Notes are per-object, so two writers attesting DIFFERENT commits is a clean
// union with nothing to resolve. Before #186 the second push was rejected
// non-fast-forward and the error discarded: the note stayed on one machine, and
// the commit read as an ungated bypass to everyone else — including the CI
// gate, which then accused the author of a bypass that never happened.
func TestPushNotes_SecondWriterStillPublishes(t *testing.T) {
	bare, a, b, shaA, shaB := twoWriters(t)

	if err := a.WriteNote(shaA, noteFor(shaA, "run-a")); err != nil {
		t.Fatal(err)
	}
	if err := a.PushNotes("origin"); err != nil {
		t.Fatalf("first writer must publish cleanly: %v", err)
	}

	// B has not fetched since; its notes ref is behind, so this push is
	// non-fast-forward — the race.
	if err := b.WriteNote(shaB, noteFor(shaB, "run-b")); err != nil {
		t.Fatal(err)
	}
	if err := b.PushNotes("origin"); err != nil {
		t.Fatalf("the second writer must reconcile and publish, not lose its note: %v", err)
	}

	// Both notes must now be on the remote. Reading through a third clone is the
	// only check that matters: a note that exists solely on the machine that
	// wrote it is not provenance anyone else can use.
	third := t.TempDir()
	if out, err := exec.Command("git", "clone", bare, third).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	r := &Repo{Dir: third}
	if err := r.FetchNotes("origin"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ sha, run string }{{shaA, "run-a"}, {shaB, "run-b"}} {
		rec, err := r.ReadNote(tc.sha)
		if err != nil {
			t.Fatalf("read note %s: %v", tc.sha, err)
		}
		if rec == nil {
			t.Errorf("note for %s never reached the remote", tc.sha)
			continue
		}
		if rec.RunID != tc.run {
			t.Errorf("note for %s = run %q, want %q", tc.sha, rec.RunID, tc.run)
		}
	}
}

// A genuine conflict — both writers attesting the SAME commit with different
// records — must be reported, not resolved.
//
// Auto-resolving would mean silently discarding one side's record of a run that
// actually happened. That is not a decision a git hook should make on somebody's
// behalf, and picking a winner quietly is how provenance stops meaning anything.
func TestPushNotes_ConflictingNoteForTheSameCommitIsReported(t *testing.T) {
	_, a, b, shaA, _ := twoWriters(t)

	// Give B the same commit as A, so both attest one object.
	gitRun(t, b.Dir, "fetch", "origin")
	if err := a.WriteNote(shaA, noteFor(shaA, "run-a")); err != nil {
		t.Fatal(err)
	}
	if err := a.PushNotes("origin"); err != nil {
		t.Fatal(err)
	}
	// B attests the same commit with a DIFFERENT record.
	if err := b.WriteNote(shaA, noteFor(shaA, "run-b-different")); err != nil {
		t.Fatal(err)
	}

	err := b.PushNotes("origin")
	if err == nil {
		t.Fatal("a conflicting note must not be silently resolved")
	}
	if !strings.Contains(err.Error(), "by hand") {
		t.Errorf("the error must tell the operator what to do: %v", err)
	}

	// The failed merge must not leave a partial state that breaks the next
	// attempt — a half-merged notes ref is worse than the rejection.
	if err := b.WriteNote(shaA, noteFor(shaA, "run-b-again")); err != nil {
		t.Errorf("the repo must still be usable after a conflicting merge: %v", err)
	}
}

// The happy path must not have grown a fetch: the overwhelming majority of
// pushes are uncontended, and paying a round-trip on every one of them to
// handle a rare race would be the wrong trade.
func TestPushNotes_UncontendedPushDoesNotReconcile(t *testing.T) {
	_, a, _, shaA, _ := twoWriters(t)
	if err := a.WriteNote(shaA, noteFor(shaA, "run-a")); err != nil {
		t.Fatal(err)
	}
	if err := a.PushNotes("origin"); err != nil {
		t.Fatal(err)
	}
	// The scratch ref only exists during reconciliation; its absence is the
	// observable proof that path was not taken.
	out, _ := exec.Command("git", "-C", a.Dir, "rev-parse", "--verify", notesIncomingRef).CombinedOutput()
	if !strings.Contains(string(out), "unknown revision") && !strings.Contains(string(out), "fatal") {
		t.Errorf("an uncontended push must not leave the reconciliation ref behind: %s", out)
	}
}
