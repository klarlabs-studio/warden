package forge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A fake gh that answers the calls ApprovalFor and Reachable make, selected by
// the shape of the path argument. Same rationale as gh_exec_test.go's fake: the
// job here is building correct argv and reading the reply, so stubbing exec
// would remove the behavior under test.
//
// It speaks `gh api --include`: a status line, headers, a blank line, the body.
// A status of "none" means gh never got a reply at all — it writes its own
// diagnosis to stderr and nothing to stdout, which is what a broken credential
// or a dead network actually looks like.
const fakeApprovalGH = `#!/bin/sh
reply() {
  if [ "$1" = "none" ]; then
    echo "gh: Bad credentials (HTTP 401)" >&2
    exit 1
  fi
  printf 'HTTP/2.0 %s Status\r\nContent-Type: application/json\r\n\r\n%s\n' "$1" "$2"
  exit "$3"
}
for a in "$@"; do
  case "$a" in
    repos/*/pulls)   reply "${GH_PULLS_STATUS:-200}"   "$GH_PULLS_OUT"   "${GH_PULLS_EXIT:-0}"   ;;
    */reviews)       reply "${GH_REVIEWS_STATUS:-200}" "$GH_REVIEWS_OUT" "${GH_REVIEWS_EXIT:-0}" ;;
  esac
done
# Anything else is the Reachable preflight: repos/{owner}/{repo}.
case "${GH_REPO_STATUS:-ok}" in
  ok)     reply 200 "${GH_REPO_OUT-owner/repo}" 0 ;;
  silent) exit 0 ;;                 # a gh that prints nothing at all
  *)      echo "gh: Bad credentials (HTTP 401)" >&2; exit 1 ;;
esac
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
	t.Setenv("GH_PULLS_STATUS", "200")
	t.Setenv("GH_REVIEWS_STATUS", "200")
	t.Setenv("GH_REPO_STATUS", "ok")
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
	if got.Undetermined {
		t.Errorf("a successful lookup reported itself as undetermined: %+v", got)
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
// finding for the report to make, not an error for the reader to debug — and
// it must survive the new undetermined state, or the real finding becomes
// noise.
func TestApprovalFor_NoPullRequestIsNotAnError(t *testing.T) {
	g := withApprovalGH(t, `[]`, `[]`)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("ApprovalFor: %v", err)
	}
	if got.Found {
		t.Errorf("reported a pull request that does not exist: %+v", got)
	}
	if got.Undetermined {
		t.Errorf("a 200 with an empty list IS an answer; reported it as undetermined: %+v", got)
	}
}

// THE REGRESSION. A forge that cannot answer used to come back as the zero
// Approval, indistinguishable from "this commit never went through a pull
// request" — so `warden evidence --approvals` published, as fact, that every
// change in the period bypassed review. It has to say it does not know.
func TestApprovalFor_AForgeThatCannotAnswerIsUndeterminedNotUnapproved(t *testing.T) {
	g := withApprovalGH(t, ``, ``)
	t.Setenv("GH_PULLS_STATUS", "none") // gh never reached the API

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("ApprovalFor: %v", err)
	}
	if !got.Undetermined {
		t.Fatalf("a forge that did not answer was recorded as an answer: %+v", got)
	}
	if got.Found {
		t.Errorf("invented a pull request: %+v", got)
	}
	if !strings.Contains(got.Reason, "Bad credentials") {
		t.Errorf("reason = %q, want gh's own diagnosis", got.Reason)
	}
}

// Rate limits and 5xx are the mid-run case the preflight cannot cover: commit
// 400 of 900 fails and the other 899 are still good evidence.
func TestApprovalFor_HTTPFailureStatusesAreUndetermined(t *testing.T) {
	for _, status := range []string{"401", "403", "429", "500", "502"} {
		t.Run(status, func(t *testing.T) {
			g := withApprovalGH(t, `{"message":"nope"}`, ``)
			t.Setenv("GH_PULLS_STATUS", status)
			t.Setenv("GH_PULLS_EXIT", "1")

			got, err := g.ApprovalFor(context.Background(), "deadbeef")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Undetermined {
				t.Errorf("HTTP %s recorded as an answer: %+v", status, got)
			}
		})
	}
}

// A 404/422 is the forge answering: it has no such commit, so the commit
// cannot have arrived through a pull request there. Preflight has already
// proven the repository itself readable, so this is a real finding and must
// not be softened into "could not tell" — an exception list where every row
// says "unknown" is a list nobody acts on.
func TestApprovalFor_ForgeSaysNoSuchCommitIsAFinding(t *testing.T) {
	for _, status := range []string{"404", "422"} {
		t.Run(status, func(t *testing.T) {
			g := withApprovalGH(t, `{"message":"No commit found"}`, ``)
			t.Setenv("GH_PULLS_STATUS", status)
			t.Setenv("GH_PULLS_EXIT", "1")

			got, err := g.ApprovalFor(context.Background(), "deadbeef")
			if err != nil {
				t.Fatal(err)
			}
			if got.Undetermined {
				t.Errorf("HTTP %s is the forge answering; reported as undetermined: %+v", status, got)
			}
			if got.Found {
				t.Errorf("invented a pull request: %+v", got)
			}
		})
	}
}

// A 200 whose body is not the shape we asked for is not an answer either.
func TestApprovalFor_UnparseableBodyIsUndetermined(t *testing.T) {
	g := withApprovalGH(t, `not json at all`, ``)

	got, err := g.ApprovalFor(context.Background(), "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Undetermined {
		t.Errorf("garbage parsed as an answer: %+v", got)
	}
}

// The pull request is known even when its reviews cannot be read — but the
// record must say the approvers are unknown rather than presenting an empty
// list, which the summary would count as "nobody approved this".
func TestApprovalFor_KeepsThePRWhenReviewsCannotBeRead(t *testing.T) {
	g := withApprovalGH(t, `[{"number":9,"author":"alice"}]`, ``)
	t.Setenv("GH_REVIEWS_STATUS", "none")

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
	if !got.Undetermined {
		t.Error("unreadable reviews reported as a pull request nobody approved")
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

func TestSplitHTTPResponse(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantStatus int
		wantBody   string
	}{
		{"crlf headers", "HTTP/2.0 200 OK\r\nX: y\r\n\r\n[1,2]\n", 200, "[1,2]"},
		{"lf headers", "HTTP/1.1 404 Not Found\nX: y\n\n{}\n", 404, "{}"},
		{"no body", "HTTP/2.0 204 No Content\r\n\r\n", 204, ""},
		{"no status line at all", "", 0, ""},
		{"gh wrote a diagnosis only", "something went wrong", 0, ""},
		{"truncated status line", "HTTP/2.0\r\n\r\nbody", 0, ""},
		{"non-numeric status", "HTTP/2.0 OK fine\r\n\r\nbody", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := splitHTTPResponse(c.in)
			if status != c.wantStatus || body != c.wantBody {
				t.Errorf("splitHTTPResponse(%q) = %d, %q; want %d, %q",
					c.in, status, body, c.wantStatus, c.wantBody)
			}
		})
	}
}

// Reachable is the preflight that stands between a broken credential and a
// signed document full of invented findings.
func TestReachable(t *testing.T) {
	g := withApprovalGH(t, `[]`, `[]`)
	if err := g.Reachable(context.Background()); err != nil {
		t.Errorf("a working gh reported unreachable: %v", err)
	}

	t.Setenv("GH_REPO_STATUS", "broken")
	err := g.Reachable(context.Background())
	if err == nil {
		t.Fatal("a gh that cannot read the repository reported reachable")
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error = %q, want gh's own account of the cause", err)
	}
}

// gh exiting 0 while naming no repository is not a confirmation.
func TestReachable_EmptyAnswerIsNotReachable(t *testing.T) {
	g := withApprovalGH(t, `[]`, `[]`)
	t.Setenv("GH_REPO_OUT", " ")
	if err := g.Reachable(context.Background()); err == nil {
		t.Error("an empty answer was accepted as a reachable forge")
	}
}

// A gh that exits 0 having printed nothing is not a confirmation either — and
// catching it in the preflight is what stops a report of 900 undetermined rows.
func TestReachable_SilentGHIsNotReachable(t *testing.T) {
	g := withApprovalGH(t, `[]`, `[]`)
	t.Setenv("GH_REPO_STATUS", "silent")
	if err := g.Reachable(context.Background()); err == nil {
		t.Error("a gh that printed no HTTP response was accepted as reachable")
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"\n\n  \n", ""},
		{"  gh: Bad credentials (HTTP 401)\nmore\n", "gh: Bad credentials (HTTP 401)"},
		{"\nsecond line wins when the first is blank", "second line wins when the first is blank"},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
