package kernel

import (
	"testing"

	axidomain "go.klarlabs.de/axi/domain"
	"go.klarlabs.de/warden/internal/domain"
)

// TestStepEffect verifies the kernel's axi effect level derives from the domain
// WritesTree predicate — the single source of truth shared with the scheduler,
// so the two can never drift. push is external; tree-writers are write-local;
// read-only checks are read-local.
func TestStepEffect(t *testing.T) {
	p := domain.ResolvedPolicy{
		AutoFix:    map[domain.StepName]int{domain.StepLint: 1},
		WriteSteps: map[domain.StepName]bool{"codegen": true},
	}
	cases := map[domain.StepName]axidomain.EffectLevel{
		domain.StepPush:   axidomain.EffectWriteExternal,
		domain.StepRebase: axidomain.EffectWriteLocal, // history rewrite
		domain.StepReview: axidomain.EffectWriteLocal, // agent may edit
		domain.StepLint:   axidomain.EffectWriteLocal, // has an auto-fix budget
		"codegen":         axidomain.EffectWriteLocal, // declared writer
		domain.StepTest:   axidomain.EffectReadLocal,  // read-only check
		"plain":           axidomain.EffectReadLocal,  // ordinary custom command
	}
	for s, want := range cases {
		if got := stepEffect(s, p); got != want {
			t.Errorf("stepEffect(%s) = %q, want %q", s, got, want)
		}
	}
}
