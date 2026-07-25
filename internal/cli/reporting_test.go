package cli

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// A split policy that keeps pre-commit fast must not let the pass line imply
// the whole tree is green — the deferred steps have to be named in the same
// breath as the ones that passed.
func TestPassLine(t *testing.T) {
	tests := []struct {
		name    string
		ran     []domain.StepName
		prePush []domain.StepName
		want    string
	}{
		{
			name:    "split policy names what ran and what is deferred",
			ran:     []domain.StepName{"lint"},
			prePush: []domain.StepName{"test", "lint"},
			want:    "warden: pre-commit passed (lint) — test runs at pre-push.",
		},
		{
			name:    "several deferred steps are listed in pre-push order",
			ran:     []domain.StepName{"lint"},
			prePush: []domain.StepName{"rebase", "lint", "security-scan", "test"},
			want:    "warden: pre-commit passed (lint) — rebase, security-scan, test run at pre-push.",
		},
		{
			name:    "nothing deferred keeps the line short",
			ran:     []domain.StepName{"lint", "test"},
			prePush: []domain.StepName{"lint"},
			want:    "warden: pre-commit passed (lint, test).",
		},
		{
			name:    "an unreadable pre-push list still reports the pass",
			ran:     []domain.StepName{"lint"},
			prePush: nil,
			want:    "warden: pre-commit passed (lint).",
		},
		{
			name:    "an unknown step list degrades to the unqualified line",
			ran:     nil,
			prePush: []domain.StepName{"test"},
			want:    "warden: pre-commit passed.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := passLine(tc.ran, tc.prePush); got != tc.want {
				t.Errorf("passLine() =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// `warden watch` should surface exactly where the split policy leaves a gap,
// and stay quiet everywhere else.
func TestWatchTip(t *testing.T) {
	armed := map[domain.Hook]bool{domain.PreCommit: true, domain.PrePush: true}

	got := watchTip(armed, []domain.StepName{"lint"}, []domain.StepName{"test", "lint"})
	if !strings.Contains(got, "warden watch") || !strings.Contains(got, "(test)") {
		t.Errorf("deferred step should surface watch: %q", got)
	}

	// Nothing deferred → the tip is noise.
	if got := watchTip(armed, []domain.StepName{"lint", "test"}, []domain.StepName{"lint"}); got != "" {
		t.Errorf("nothing deferred should be silent, got %q", got)
	}

	// With no pre-commit shim there is nothing to defer *from*.
	pushOnly := map[domain.Hook]bool{domain.PrePush: true}
	if got := watchTip(pushOnly, []domain.StepName{"lint"}, []domain.StepName{"test", "lint"}); got != "" {
		t.Errorf("no armed pre-commit should be silent, got %q", got)
	}
}

// The hook pin is a bootstrap floor, not a lock: a PATH binary wins. Status has
// to say so when the two disagree, and stay quiet when they don't.
func TestPinSkewLine(t *testing.T) {
	skew := pinSkewLine(map[domain.Hook]string{
		domain.PreCommit: "0.17.0",
		domain.PrePush:   "0.17.0",
	}, "0.18.16")
	for _, want := range []string{"pre-commit pins 0.17.0", "pre-push pins 0.17.0", "0.18.16 is what runs"} {
		if !strings.Contains(skew, want) {
			t.Errorf("skew line %q missing %q", skew, want)
		}
	}

	// Matching pin → silence.
	if got := pinSkewLine(map[domain.Hook]string{domain.PreCommit: "0.18.16"}, "0.18.16"); got != "" {
		t.Errorf("matching pin should be silent, got %q", got)
	}
	// No hooks installed / no pin recorded → silence.
	if got := pinSkewLine(nil, "0.18.16"); got != "" {
		t.Errorf("absent pins should be silent, got %q", got)
	}
	// Only the diverging hook is named.
	got := pinSkewLine(map[domain.Hook]string{
		domain.PreCommit: "0.18.16",
		domain.PrePush:   "0.17.0",
	}, "0.18.16")
	if strings.Contains(got, "pre-commit") || !strings.Contains(got, "pre-push pins 0.17.0") {
		t.Errorf("only the skewed hook should be named: %q", got)
	}
}
