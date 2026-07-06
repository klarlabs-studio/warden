package cli

import (
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// TestShouldNotify pins the desktop-notification policy:
//   - on by default, silenced only by `notify: false`;
//   - a PASSING run notifies only after it ran long enough (default 10s, or the
//     configured notify_after) that the developer may have tabbed away — a fast
//     green gate stays quiet (the over-firing bug this fixes);
//   - a FAILED/blocked push always notifies, however fast, so a stopped push is
//     never missed;
//   - a malformed notify_after falls back to the default rather than erroring.
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

		// Malformed notify_after → default 10s threshold.
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

// TestNotifyThreshold checks the notify_after resolution: configured value wins,
// empty and malformed both fall back to the default.
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
