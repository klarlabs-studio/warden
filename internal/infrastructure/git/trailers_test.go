package git

import (
	"reflect"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// commitMsg makes an empty commit with message msg and returns its SHA.
func commitMsg(t *testing.T, dir, msg string) string {
	t.Helper()
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return gitRev(t, dir, "HEAD")
}

func openRepo(t *testing.T, dir string) *Repo {
	t.Helper()
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A multi-commit span reads every commit in (base, head], newest first, with
// the trailers git's own parser found: odd spacing trimmed, a key's spelling
// kept, continuation lines unfolded, and a trailer-looking line that is not in
// the final paragraph not reported at all.
func TestSpanTrailers_MultiCommitSpan(t *testing.T) {
	dir := newTestRepo(t)
	// log.showSignature would print verification output into the format.
	gitRun(t, dir, "config", "log.showSignature", "true")
	base := gitRev(t, dir, "HEAD")
	plain := commitMsg(t, dir, "plain commit\n\nCo-Authored-By: not a trailer, this paragraph continues\nwith prose after it.\n\nThe end.")
	odd := commitMsg(t, dir, "odd spacing\n\nbody\n\nco-authored-by:    Claude Opus 5   <noreply@anthropic.com>   \nSigned-off-by: warden-test")
	session := commitMsg(t, dir, "session link\n\nClaude-Session: https://claude.ai/code/session_X\nNote: folded\n  across lines")

	got, err := openRepo(t, dir).SpanTrailers(base, session)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.CommitTrailers{
		{Commit: session, Trailers: []domain.Trailer{
			{Key: "Claude-Session", Value: "https://claude.ai/code/session_X"},
			{Key: "Note", Value: "folded across lines"},
		}},
		{Commit: odd, Trailers: []domain.Trailer{
			{Key: "co-authored-by", Value: "Claude Opus 5   <noreply@anthropic.com>"},
			{Key: "Signed-off-by", Value: "warden-test"},
		}},
		{Commit: plain},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("span trailers:\n got %+v\nwant %+v", got, want)
	}
}

// A merge brings in the merged branch's commits and the merge commit itself;
// both are in the span and both are read.
func TestSpanTrailers_MergeCommits(t *testing.T) {
	dir := newTestRepo(t)
	base := gitRev(t, dir, "HEAD")
	gitRun(t, dir, "switch", "-q", "-c", "side")
	side := commitMsg(t, dir, "side work\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	gitRun(t, dir, "switch", "-q", "main")
	mainline := commitMsg(t, dir, "mainline work")
	gitRun(t, dir, "merge", "-q", "--no-ff", "side", "-m", "merge side\n\nClaude-Session: https://claude.ai/code/session_Y")
	merge := gitRev(t, dir, "HEAD")

	got, err := openRepo(t, dir).SpanTrailers(base, merge)
	if err != nil {
		t.Fatal(err)
	}
	byCommit := map[string][]domain.Trailer{}
	for _, c := range got {
		byCommit[c.Commit] = c.Trailers
	}
	if len(got) != 3 || len(byCommit) != 3 {
		t.Fatalf("want the merge, the side commit and the mainline commit, got %+v", got)
	}
	if want := []domain.Trailer{{Key: "Claude-Session", Value: "https://claude.ai/code/session_Y"}}; !reflect.DeepEqual(byCommit[merge], want) {
		t.Errorf("merge trailers = %+v, want %+v", byCommit[merge], want)
	}
	if want := []domain.Trailer{{Key: "Co-Authored-By", Value: "Claude <noreply@anthropic.com>"}}; !reflect.DeepEqual(byCommit[side], want) {
		t.Errorf("side trailers = %+v, want %+v", byCommit[side], want)
	}
	if tr, ok := byCommit[mainline]; !ok || len(tr) != 0 {
		t.Errorf("mainline commit must be read with no trailers, got %+v (present %v)", tr, ok)
	}
}

// With no span claimed the record attests the tip alone, so only the tip is
// read, never its ancestors.
func TestSpanTrailers_NoBaseReadsTheTipOnly(t *testing.T) {
	dir := newTestRepo(t)
	commitMsg(t, dir, "older\n\nClaude-Session: https://claude.ai/code/session_old")
	tip := commitMsg(t, dir, "tip")
	got, err := openRepo(t, dir).SpanTrailers("", tip)
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.CommitTrailers{{Commit: tip}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want only the tip %+v", got, want)
	}
	if got, err := openRepo(t, dir).SpanTrailers("", ""); err != nil || got != nil {
		t.Fatalf("no head reads nothing, got %+v %v", got, err)
	}
}

// An empty range is empty, not an error.
func TestSpanTrailers_EmptySpan(t *testing.T) {
	dir := newTestRepo(t)
	head := gitRev(t, dir, "HEAD")
	got, err := openRepo(t, dir).SpanTrailers(head, head)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v %v, want nothing", got, err)
	}
}

func TestParseSpanTrailers(t *testing.T) {
	out := "aaa\x1fK\x1fv\x1eK2\x1f v2 \x1e\n\x00\nbbb\x1f\n\x00ccc\x1fnokv\x1e\x1fvalue-without-key\x00"
	want := []domain.CommitTrailers{
		{Commit: "aaa", Trailers: []domain.Trailer{{Key: "K", Value: "v"}, {Key: "K2", Value: "v2"}}},
		{Commit: "bbb"},
		{Commit: "ccc"},
	}
	if got := parseSpanTrailers(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
