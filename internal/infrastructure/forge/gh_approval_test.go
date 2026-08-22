package forge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A fake gh that answers the two API calls ApprovalFor makes, selected by the
// tail of the path. Same rationale as gh_exec_test.go's fake: the job here is
// building correct argv and reading the reply, so stubbing exec would remove
// the behavior under test.
const fakeApprovalGH = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    */pulls)   printf '%s\n' "$GH_PULLS_OUT";   exit "${GH_PULLS_EXIT:-0}" ;;
    */reviews) printf '%s\n' "$GH_REVIEWS_OUT"; exit "${GH_REVIEWS_EXIT:-0}" ;;
  esac
done
exit 0
`

func withApprovalGH(t *testing.T, pulls, reviews string) *GH {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script; warden is unix-first")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeApprovalGH), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_PULLS_OUT", pulls)
	t.Setenv("GH_REVIEWS_OUT", reviews)
	t.Setenv("GH_PULLS_EXIT", "0")
	t.Setenv("GH_REVIEWS_EXIT", "0")
	return NewGH(t.TempDir())
}

func TestApprovalFor_ReadsThePullRequestAndItsApprovers(t *testing.T) {
	g := withApprovalGH(t,
		`[{"number":222,"author":"alice"}]`,
		`["bob","carol"]`)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("ApprovalFor: %v", err)
	}
	if !got.Found || got.PR != 222 || got.Author != "alice" {
		t.Fatalf("got %+v, want PR 222 by alice", got)
	}
	if len(got.Approvers) != 2 {
		t.Errorf("approvers = %v, want two", got.Approvers)
	}
	if !got.Independent() {
		t.Error("an approval by someone other than the author should be independent")
	}
}

// One person approving twice is one approver. Counting submissions instead of
// people would turn a self-approval loop into apparent consensus.
func TestApprovalFor_DedupesRepeatedApprovals(t *testing.T) {
	g := withApprovalGH(t, `[{"number":7,"author":"alice"}]`, `["bob","bob","bob"]`)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Approvers) != 1 {
		t.Errorf("approvers = %v, want one", got.Approvers)
	}
}

// A commit pushed straight to the branch has no pull request. That is a
// finding for the report to make, not an error for the reader to debug.
func TestApprovalFor_NoPullRequestIsNotAnError(t *testing.T) {
	g := withApprovalGH(t, `[]`, `[]`)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("ApprovalFor: %v", err)
	}
	if got.Found {
		t.Errorf("reported a pull request that does not exist: %+v", got)
	}
}

// The pull request is known even when its reviews cannot be read; reporting
// "no approvers" there would assert something the forge never said.
func TestApprovalFor_KeepsThePRWhenReviewsCannotBeRead(t *testing.T) {
	g := withApprovalGH(t, `[{"number":9,"author":"alice"}]`, ``)
	t.Setenv("GH_REVIEWS_EXIT", "1")

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.PR != 9 {
		t.Errorf("lost the pull request: %+v", got)
	}
	if len(got.Approvers) != 0 {
		t.Errorf("invented approvers: %v", got.Approvers)
	}
}

// The commit's own pull request is the first entry; later ones are forks and
// back-ports that merely contain it.
func TestApprovalFor_TakesTheFirstPullRequest(t *testing.T) {
	g := withApprovalGH(t,
		`[{"number":10,"author":"alice"},{"number":11,"author":"mallory"}]`,
		`["bob"]`)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if got.PR != 10 || got.Author != "alice" {
		t.Errorf("got PR %d by %s, want 10 by alice", got.PR, got.Author)
	}
}

func TestItoa(t *testing.T) {
	for n, want := range map[int]string{0: "0", 7: "7", 42: "42", 12345: "12345"} {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q", n, got)
		}
	}
}
