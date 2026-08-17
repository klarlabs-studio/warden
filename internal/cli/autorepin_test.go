package cli

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// An upgrade used to leave every armed shim pinning the previous version, and
// the shim reported that on every run until someone ran `warden hooks repin` by
// hand. The notice described a difference that changed nothing -- a warden on
// PATH runs whatever the pin records -- so it was a permanent warning with no
// action behind it.
//
// These cover autoRepinTargets rather than autoRepin, because the decision is
// the part worth pinning down: which hooks move, and in which direction.
func TestAutoRepinTargets(t *testing.T) {
	armedBoth := map[domain.Hook]bool{domain.PreCommit: true, domain.PrePush: true}

	tests := []struct {
		name      string
		installed map[domain.Hook]bool
		pins      map[domain.Hook]string
		running   string
		want      []domain.Hook
	}{
		{
			name:      "upgrade repins every armed hook",
			installed: armedBoth,
			pins:      map[domain.Hook]string{domain.PreCommit: "0.26.0", domain.PrePush: "0.26.0"},
			running:   "0.27.0",
			want:      []domain.Hook{domain.PreCommit, domain.PrePush},
		},
		{
			name:      "same version is left alone",
			installed: armedBoth,
			pins:      map[domain.Hook]string{domain.PreCommit: "0.27.0", domain.PrePush: "0.27.0"},
			running:   "0.27.0",
			want:      nil,
		},
		{
			// The asymmetry this exists for. A checkout with no warden on PATH
			// downloads exactly the pinned version, so pinning backwards would
			// turn one developer running an old binary into a repository that
			// fetches an old binary for everyone.
			name:      "downgrade never repins",
			installed: armedBoth,
			pins:      map[domain.Hook]string{domain.PreCommit: "0.27.0", domain.PrePush: "0.27.0"},
			running:   "0.26.0",
			want:      nil,
		},
		{
			name:      "a disarmed hook is never armed by repinning",
			installed: map[domain.Hook]bool{domain.PreCommit: true},
			pins:      map[domain.Hook]string{domain.PreCommit: "0.26.0", domain.PrePush: "0.26.0"},
			running:   "0.27.0",
			want:      []domain.Hook{domain.PreCommit},
		},
		{
			name:      "an unpinned armed hook gets its first pin",
			installed: map[domain.Hook]bool{domain.PreCommit: true},
			pins:      map[domain.Hook]string{},
			running:   "0.27.0",
			want:      []domain.Hook{domain.PreCommit},
		},
		{
			name:      "patch upgrades count",
			installed: map[domain.Hook]bool{domain.PrePush: true},
			pins:      map[domain.Hook]string{domain.PrePush: "0.27.0"},
			running:   "0.27.1",
			want:      []domain.Hook{domain.PrePush},
		},
		{
			name:      "nothing armed, nothing to do",
			installed: map[domain.Hook]bool{},
			pins:      map[domain.Hook]string{domain.PreCommit: "0.1.0"},
			running:   "9.9.9",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autoRepinTargets(tt.installed, tt.pins, tt.running)
			if len(got) != len(tt.want) {
				t.Fatalf("targets = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("targets[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// autoRepin only moves forward; `warden hooks repin` moves in either direction
// because a person asked for it. If these two ever collapse into one function,
// the downgrade protection goes with it.
func TestAutoRepinIsNarrowerThanExplicitRepin(t *testing.T) {
	installed := map[domain.Hook]bool{domain.PreCommit: true}
	pins := map[domain.Hook]string{domain.PreCommit: "0.27.0"}
	const running = "0.26.0" // a downgrade

	if got := autoRepinTargets(installed, pins, running); len(got) != 0 {
		t.Errorf("autoRepinTargets repinned on a downgrade: %v", got)
	}
	if got := repinTargets(installed, pins, running); len(got) != 1 {
		t.Errorf("explicit repin should still honor a deliberate downgrade, got %v", got)
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.27.0", "0.26.0", true},
		{"0.26.0", "0.27.0", false},
		{"0.27.0", "0.27.0", false},
		{"0.27.1", "0.27.0", true},
		{"1.0.0", "0.99.99", true},
		{"v0.27.0", "0.26.0", true}, // leading v tolerated
		{"0.27.0", "v0.26.0", true}, // on either side
		{"0.27.0", "", true},        // unpinned takes its first pin
		{"", "0.27.0", false},       // unparseable is never newer
		{"0.27.0-rc1", "0.26.0", true},
	}

	for _, tt := range tests {
		if got := domain.IsNewer(tt.a, tt.b); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
