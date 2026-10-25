package kernel

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// The executor is what applies a step's deadline, so it is the one place that
// can record the deadline as observed rather than as configured.
func TestExecutorRecordsTheDeadlineItApplied(t *testing.T) {
	for _, timeout := range []time.Duration{0, 5 * time.Minute} {
		lint := &fakeStep{name: domain.StepLint, status: domain.StepPass}
		policy := policyFor(domain.StepLint)
		if timeout > 0 {
			policy.Timeouts = map[domain.StepName]time.Duration{domain.StepLint: timeout}
		}
		k, err := newFactory(lint).New(policy, application.StepContext{}, new([]domain.Finding), nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := k.Execute(context.Background(), domain.StepLint)
		if err != nil {
			t.Fatal(err)
		}
		if out.Result.Deadline != timeout {
			t.Errorf("timeout %s: recorded deadline %s", timeout, out.Result.Deadline)
		}
	}
}
