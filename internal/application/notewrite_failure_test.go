package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// A gated push whose note could not be WRITTEN must say so.
//
// The note-push failure already warns (#186). The note-WRITE failure is the
// strictly worse version of the same event — there is no note at all, not even
// locally — and it is the documented failure mode: `git notes add` needs a
// committer identity, and a machine without one fails here every time.
func TestRunner_PrePushWarnsWhenTheNoteCouldNotBeWritten(t *testing.T) {
	git := &fakeGit{
		root: t.TempDir(), branch: "main", head: "sha1",
		wt:           &fakeWorktree{dir: "/wt", headSHA: "sha1"},
		writeNoteErr: errors.New("unable to auto-detect email address"),
	}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())

	res, err := r.Run(context.Background(), domain.PrePush)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed {
		t.Fatalf("outcome = %s, want passed (provenance is best-effort in the gate path)", res.Outcome)
	}
	if git.wroteNote {
		t.Fatal("precondition: the note write was supposed to fail")
	}
	// The artifact, not the exit status: the run must not report a clean gated
	// push while the commit carries no provenance anybody can read. Match on the
	// underlying git failure, which only a warning about THIS event can carry —
	// the run also warns that the note is unsigned, and that is a different event.
	var found bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "unable to auto-detect email address") {
			found = true
		}
	}
	if !found {
		t.Errorf("a run that wrote no note reported no warning about it.\n  message  = %q\n  warnings = %q",
			res.Message, res.Warnings)
	}
}
