package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// TestNoteGitPushError pins the pre-push heads-up: on a passing run warden warns
// that git's imminent "failed to push some refs" is expected (warden already
// pushed the gated commit and fails the hook on purpose), and on any non-pass it
// stays silent so git's genuinely-correct error stands on its own.
func TestNoteGitPushError(t *testing.T) {
	cases := []struct {
		outcome domain.Outcome
		wantOut bool
	}{
		{domain.OutcomePassed, true},
		{domain.OutcomeFailed, false},
		{domain.OutcomeRejected, false},
		{domain.OutcomeAborted, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			var buf bytes.Buffer
			noteGitPushError(&buf, application.RunResult{Outcome: tc.outcome})
			got := strings.Contains(buf.String(), "failed to push some refs")
			if got != tc.wantOut {
				t.Errorf("outcome %s: printed=%v, want %v (output %q)", tc.outcome, got, tc.wantOut, buf.String())
			}
		})
	}
}

// TestPushGatable pins which git pre-push payloads warden gates. Git feeds a
// pre-push hook one line per pushed ref — "<local ref> <local sha> <remote ref>
// <remote sha>" — so warden gates only when a branch (refs/heads/*) is being
// created or updated. A notes-only push, a tag, a lone branch deletion (all-zero
// local sha), or an unrelated ref advances no branch: nothing to gate, let git
// complete it. It fails safe toward gating: an empty payload or a set with no
// well-formed ref line (a manual run, a test) is gated, never silently skipped.
func TestPushGatable(t *testing.T) {
	const zero = "0000000000000000000000000000000000000000"
	cases := []struct {
		name  string
		stdin string
		want  bool
	}{
		{"branch update", "refs/heads/main abc123 refs/heads/main def456", true},
		{"branch create", "refs/heads/feat abc123 refs/heads/feat " + zero, true},
		{"branch delete", "(delete) " + zero + " refs/heads/old " + zero, false},
		{"notes ref only", "refs/notes/warden abc123 refs/notes/warden def456", false},
		{"tag only", "refs/tags/v0.16.0 abc123 refs/tags/v0.16.0 " + zero, false},
		{"notes plus branch", "refs/notes/warden a1 refs/notes/warden a2\nrefs/heads/main b1 refs/heads/main b2", true},
		{"empty fails safe to gating", "", true},
		{"no well-formed ref line fails safe to gating", "\n   \nrefs/heads/main\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pushGatable(strings.NewReader(tc.stdin))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("pushGatable(%q) = %v, want %v", tc.stdin, got, tc.want)
			}
		})
	}
}

// TestShouldNotify pins the desktop-notification policy:
//   - on by default, silenced only by `notify: false`;
//   - a PASSING run notifies only after it ran long enough (default 10s, or the
//     configured notify_after) that the developer may have tabbed away — a fast
//     green gate stays quiet (the over-firing bug this fixes);
//   - a FAILED/blocked push always notifies, however fast, so a stopped push is
//     never missed;
//   - a malformed notify_after is rejected at config load (Config.Validate); the
//     helper still falls back defensively should one reach it programmatically.
func TestShouldNotify(t *testing.T) {
	on, off := true, false
	pass, fail := domain.OutcomePassed, domain.OutcomeFailed
	dfltLong := notifyAfter                     // exactly the default threshold
	dfltShort := notifyAfter - time.Millisecond // just under it

	cases := []struct {
		name    string
		cfg     domain.Config
		outcome domain.Outcome
		elapsed time.Duration
		want    bool
	}{
		{"default long pass notifies", domain.Config{}, pass, dfltLong, true},
		{"default fast pass stays quiet", domain.Config{}, pass, dfltShort, false},
		{"fast FAIL always notifies", domain.Config{}, fail, time.Millisecond, true},
		{"disabled never notifies (even fail)", domain.Config{Notify: &off}, fail, time.Hour, false},
		{"enabled fast pass still gated", domain.Config{Notify: &on}, pass, dfltShort, false},

		// Configurable threshold.
		{"notify_after=1m: 30s pass quiet", domain.Config{NotifyAfter: "1m"}, pass, 30 * time.Second, false},
		{"notify_after=1m: 60s pass notifies", domain.Config{NotifyAfter: "1m"}, pass, time.Minute, true},
		{"notify_after=2s: 3s pass notifies", domain.Config{NotifyAfter: "2s"}, pass, 3 * time.Second, true},

		// Defense-in-depth: a malformed value is rejected by Config.Validate at
		// load, but if one is constructed past it the helper falls back to 10s.
		{"bad notify_after falls back: 5s pass quiet", domain.Config{NotifyAfter: "nonsense"}, pass, 5 * time.Second, false},
		{"bad notify_after falls back: 10s pass notifies", domain.Config{NotifyAfter: "nonsense"}, pass, notifyAfter, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldNotify(tc.cfg, tc.outcome, tc.elapsed); got != tc.want {
				t.Errorf("shouldNotify(%+v, %s, %s) = %v, want %v", tc.cfg, tc.outcome, tc.elapsed, got, tc.want)
			}
		})
	}
}

// TestNotifyThreshold checks the notify_after resolution: a configured value
// wins; empty falls back to the default. Malformed/negative inputs are rejected
// upstream by Config.Validate, but the helper stays defensive and also falls
// back on them, which these cases pin.
func TestNotifyThreshold(t *testing.T) {
	cases := []struct {
		notifyAfterCfg string
		want           time.Duration
	}{
		{"", notifyAfter},
		{"30s", 30 * time.Second},
		{"2m", 2 * time.Minute},
		{"bogus", notifyAfter},
		{"-5s", notifyAfter}, // negative rejected → default
	}
	for _, tc := range cases {
		if got := notifyThreshold(domain.Config{NotifyAfter: tc.notifyAfterCfg}); got != tc.want {
			t.Errorf("notifyThreshold(%q) = %s, want %s", tc.notifyAfterCfg, got, tc.want)
		}
	}
}
