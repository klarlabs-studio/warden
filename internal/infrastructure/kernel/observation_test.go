package kernel

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestInitialObservationEvidence(t *testing.T) {
	o := domain.ParseContainerObservation("routes=0\nroutes6=0\n")
	for _, outcome := range []domain.StepOutcome{domain.StepExecuted, domain.StepCached, domain.StepSkipped, domain.StepPredicted} {
		result := domain.StepResult{Step: domain.StepTest, Status: domain.StepPass, Outcome: outcome, Environment: &domain.ExecutionEnvironment{}, Observed: &o}
		found := false
		for _, evidence := range evidenceFor(result) {
			if evidence.Kind == "test.observation" {
				found = true
			}
		}
		if found != (outcome == domain.StepExecuted) {
			t.Fatalf("%s observation present=%v", outcome, found)
		}
	}
}
