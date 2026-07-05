package application

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestWorktreeRegistry(t *testing.T) {
	reg := newWorktreeRegistry()

	// Unassigned steps resolve to "" (the caller uses the canonical worktree).
	if got := reg.dirFor(domain.StepTest); got != "" {
		t.Errorf("unassigned step should resolve to \"\", got %q", got)
	}

	reg.set(domain.StepTest, "/wt-test")
	reg.set(domain.StepLint, "/wt-lint")
	if got := reg.dirFor(domain.StepTest); got != "/wt-test" {
		t.Errorf("dirFor(test) = %q, want /wt-test", got)
	}
	if got := reg.dirFor(domain.StepLint); got != "/wt-lint" {
		t.Errorf("dirFor(lint) = %q, want /wt-lint", got)
	}

	// reset clears every assignment (back to the canonical worktree).
	reg.reset()
	if got := reg.dirFor(domain.StepTest); got != "" {
		t.Errorf("after reset, dirFor(test) = %q, want \"\"", got)
	}
}
