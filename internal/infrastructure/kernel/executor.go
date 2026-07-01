// Package kernel wires Warden's pipeline onto the axi-go execution kernel:
// each step becomes an axi Action bound to an ActionExecutor, effect profiles
// gate the terminal push on approval, and evidence records flow back for the
// run-level tamper-evident chain (§4.4).
package kernel

import (
	"context"
	"fmt"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// resultKey is the ExecutionResult.Data key under which a step's normalized
// StepResult is carried back to the runner. A step never fails the axi session
// on a policy rejection — it reports pass/fail in the result so its evidence is
// always recorded; only operational errors abort the session.
const resultKey = "step_result"

// stepExecutor adapts an application.Step to axi-go's ActionExecutor. It is
// built per run and closes over the StepContext resolved for that run, so
// per-step agent/budget/command selection is baked in at registration.
type stepExecutor struct {
	step application.Step
	sc   application.StepContext
	// priors is a pointer to the run's accumulating findings so each step sees
	// what earlier steps reported (wire protocol's prior_findings).
	priors *[]domain.Finding
}

// Execute runs the wrapped step and converts its outcome into an axi
// ExecutionResult plus evidence records. A non-nil error is reserved for
// operational failures (the step could not run); a policy rejection is carried
// as StepFail inside the result so the runner, not the kernel, owns the
// abort decision and the evidence is still recorded.
func (e stepExecutor) Execute(ctx context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	sc := e.sc
	if e.priors != nil {
		sc.PriorFindings = *e.priors
	}

	res, err := e.step.Run(ctx, sc)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("step %s: %w", e.step.Name(), err)
	}
	if e.priors != nil {
		*e.priors = append(*e.priors, res.Findings...)
	}

	return axidomain.ExecutionResult{
			Data:    map[string]any{resultKey: res},
			Summary: res.Summary,
		},
		evidenceFor(res),
		nil
}

// evidenceFor turns a step result into evidence records: one summary record
// for the step outcome plus one per finding, so the run-level chain captures
// exactly what each step observed.
func evidenceFor(res domain.StepResult) []axidomain.EvidenceRecord {
	records := []axidomain.EvidenceRecord{{
		Kind:   fmt.Sprintf("%s.%s", res.Step, res.Status),
		Source: string(res.Step),
		Value: map[string]any{
			"status": string(res.Status),
			"fixed":  res.Fixed,
		},
	}}
	for _, f := range res.Findings {
		records = append(records, axidomain.EvidenceRecord{
			Kind:   fmt.Sprintf("%s.finding", res.Step),
			Source: string(res.Step),
			Value: map[string]any{
				"severity": string(f.Severity),
				"message":  f.Message,
				"file":     f.File,
				"line":     f.Line,
			},
		})
	}
	return records
}

// stepResultFrom extracts the StepResult a stepExecutor stashed in an axi
// result. It returns ok=false when the data shape is unexpected.
func stepResultFrom(result *axidomain.ExecutionResult) (domain.StepResult, bool) {
	if result == nil {
		return domain.StepResult{}, false
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		return domain.StepResult{}, false
	}
	sr, ok := data[resultKey].(domain.StepResult)
	return sr, ok
}
