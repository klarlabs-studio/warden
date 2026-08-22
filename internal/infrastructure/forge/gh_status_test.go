package forge

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// A published status has to say four things correctly or it is worse than
// nothing: which commit, which context (branch protection matches on it),
// success, and a description a human can read in the checks list.
func TestPublishStatusSendsTheCommitContextAndState(t *testing.T) {
	f := installFakeGH(t)
	dir := t.TempDir()

	err := NewGH(dir).publish(context.Background(), StatusUpdate{
		SHA:         "0123456789abcdef0123456789abcdef01234567",
		State:       "success",
		Context:     "Warden provenance",
		Description: "gate passed (test, lint)",
	})
	if err != nil {
		t.Fatalf("PublishStatus: %v", err)
	}

	argv := strings.Join(f.calls(), "\n")
	if !strings.Contains(argv, "statuses/0123456789abcdef0123456789abcdef01234567") {
		t.Errorf("status not posted against the commit: %q", argv)
	}
	for _, want := range []string{"state=success", "context=Warden provenance", "description=gate passed (test, lint)"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q: %s", want, argv)
		}
	}
	if !strings.Contains(argv, "POST") {
		t.Errorf("not a POST: %s", argv)
	}
}

// GitHub truncates a description over 140 characters and returns an error for
// some overlong payloads. A step list grows without limit, so it gets clipped
// here rather than at the API.
func TestPublishStatusClipsALongDescription(t *testing.T) {
	f := installFakeGH(t)
	dir := t.TempDir()

	long := "gate passed (" + strings.Repeat("verylongstepname, ", 30) + ")"
	if err := NewGH(dir).publish(context.Background(), StatusUpdate{
		SHA: "abc", State: "success", Context: "c", Description: long,
	}); err != nil {
		t.Fatalf("PublishStatus: %v", err)
	}

	argv := strings.Join(f.calls(), "\n")
	for _, line := range strings.Split(argv, "\n") {
		if i := strings.Index(line, "description="); i >= 0 {
			if got := len(line[i+len("description="):]); got > 140 {
				t.Errorf("description length %d, want <= 140", got)
			}
			return
		}
	}
	t.Fatal("no description in argv")
}

// A commit with no sha is a programming error, not something to send to the
// API and let it 404.
func TestPublishStatusRefusesAnEmptyCommit(t *testing.T) {
	installFakeGH(t)
	dir := t.TempDir()
	if err := NewGH(dir).publish(context.Background(), StatusUpdate{
		State: "success", Context: "c",
	}); err == nil {
		t.Fatal("accepted an empty commit sha")
	}
}

// Clipping happens on runes, not bytes: a byte-wise cut through a multi-byte
// character produces invalid UTF-8, which the API rejects outright rather than
// tidying up.
func TestClipCutsWholeRunesAndStaysUnderTheLimit(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("a", 500),
		strings.Repeat("é", 500),
		strings.Repeat("日", 500),
		"short enough",
	} {
		got := clip(in, maxStatusDescription)
		if len(got) > maxStatusDescription {
			t.Errorf("clip(%q…) = %d bytes, want <= %d", in[:6], len(got), maxStatusDescription)
		}
		if !utf8.ValidString(got) {
			t.Errorf("clip produced invalid UTF-8 for %q…", in[:6])
		}
	}
	if got := clip("short enough", maxStatusDescription); got != "short enough" {
		t.Errorf("clip altered a short string: %q", got)
	}
}
