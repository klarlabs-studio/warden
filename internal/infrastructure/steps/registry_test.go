package steps

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestDefaultRegistry(t *testing.T) {
	reg := Default()

	want := []domain.StepName{
		domain.StepIntent, domain.StepRebase, domain.StepReview,
		domain.StepTest, domain.StepDocument, domain.StepLint,
	}
	if len(reg) != len(want) {
		t.Fatalf("Default() has %d steps, want %d", len(reg), len(want))
	}
	for _, name := range want {
		step, ok := reg[name]
		if !ok {
			t.Errorf("Default() missing step %s", name)
			continue
		}
		if step.Name() != name {
			t.Errorf("registry[%s].Name() = %s, want %s", name, step.Name(), name)
		}
	}
}

func TestDefaultRegistryStepTypes(t *testing.T) {
	reg := Default()

	// lint/test are deterministic shell steps; the reasoning steps are agent
	// steps; rebase is its own step. Assert the concrete wiring so a mistaken
	// swap is caught.
	if _, ok := reg[domain.StepLint].(ShellStep); !ok {
		t.Errorf("lint step = %T, want ShellStep", reg[domain.StepLint])
	}
	if _, ok := reg[domain.StepTest].(ShellStep); !ok {
		t.Errorf("test step = %T, want ShellStep", reg[domain.StepTest])
	}
	if _, ok := reg[domain.StepReview].(AgentStep); !ok {
		t.Errorf("review step = %T, want AgentStep", reg[domain.StepReview])
	}
	if _, ok := reg[domain.StepRebase].(RebaseStep); !ok {
		t.Errorf("rebase step = %T, want RebaseStep", reg[domain.StepRebase])
	}
}

func TestBuiltinNames(t *testing.T) {
	names := BuiltinNames()
	if len(names) != 6 {
		t.Fatalf("BuiltinNames() = %d names, want 6", len(names))
	}

	// Every built-in name must resolve in the default registry, and vice versa,
	// so the two lists never drift apart.
	reg := Default()
	for _, name := range names {
		if _, ok := reg[name]; !ok {
			t.Errorf("BuiltinNames lists %s, absent from Default()", name)
		}
	}
	if len(names) != len(reg) {
		t.Errorf("BuiltinNames has %d, Default has %d", len(names), len(reg))
	}
}
