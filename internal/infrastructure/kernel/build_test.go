package kernel

import (
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
)

func TestResolveStep_SecurityScanGetsTheScannerAwareStep(t *testing.T) {
	// The `security-scan` command step is the one custom step warden knows how
	// to interpret: it reads the scanner's report so it can gate on the findings
	// the change introduced rather than the repo's whole backlog. Falling back to
	// a plain ShellStep here would silently restore total-state gating.
	got, err := resolveStep(application.Registry{}, domain.StepSecurityScan,
		map[string]string{"security-scan": "nox scan . -severity-threshold high"})
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if _, ok := got.(steps.SecurityScanStep); !ok {
		t.Errorf("resolveStep returned %T, want a SecurityScanStep", got)
	}
}

func TestResolveStep_OtherCommandStepsStayPlainShell(t *testing.T) {
	got, err := resolveStep(application.Registry{}, "codegen", map[string]string{"codegen": "make gen"})
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if _, ok := got.(steps.ShellStep); !ok {
		t.Errorf("resolveStep returned %T, want a ShellStep", got)
	}
}

func TestResolveStep_RegistryStillWins(t *testing.T) {
	// A registered implementation (a test fake, a future built-in) must keep
	// precedence over the command ladder.
	fake := &fakeStep{name: domain.StepSecurityScan, status: domain.StepPass}
	got, err := resolveStep(application.Registry{domain.StepSecurityScan: fake}, domain.StepSecurityScan,
		map[string]string{"security-scan": "nox scan ."})
	if err != nil {
		t.Fatalf("resolveStep: %v", err)
	}
	if got != application.Step(fake) {
		t.Errorf("resolveStep returned %T, want the registered step", got)
	}
}
