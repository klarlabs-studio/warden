package service

import (
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// CIPlan describes eligible reuse, not jobs the forge actually skipped.
type CIPlan struct {
	Version        int             `json:"version"`
	SHA            string          `json:"sha"`
	Jobs           []CIJobDecision `json:"jobs"`
	RunJobs        []string        `json:"run_jobs"`
	ReusableJobs   []string        `json:"reusable_jobs"`
	VerificationMS int64           `json:"verification_ms"`
}

type CIJobDecision struct {
	Name           string   `json:"name"`
	Action         string   `json:"action"`
	Reasons        []string `json:"reasons"`
	VerificationMS int64    `json:"verification_ms"`
}

// PlanCI pins the requested revision once before checking jobs. Every reusable
// decision uses the existing trust, commit binding and requirements verifier.
// Errors return no plan: callers must never consume a partially computed one.
func (s *Service) PlanCI(commit string, policy domain.CIJobPolicy, keys ...string) (CIPlan, error) {
	start := time.Now()
	if err := policy.Validate(); err != nil {
		return CIPlan{}, err
	}
	if len(keys) == 0 {
		return CIPlan{}, fmt.Errorf("CI planning requires explicitly pinned trusted keys")
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return CIPlan{}, fmt.Errorf("CI planning requires non-empty trusted keys")
		}
	}
	// Verify resolves symbolic revisions using the same path as ordinary verify.
	initial, err := s.VerifyWithPolicy(commit, ExternalReject, keys...)
	if err != nil {
		return CIPlan{}, err
	}
	plan := CIPlan{Version: 1, SHA: initial.SHA, Jobs: []CIJobDecision{}, RunJobs: []string{}, ReusableJobs: []string{}}
	for _, name := range policy.OrderedJobs() {
		started := time.Now()
		decision := CIJobDecision{Name: name, Action: "run", Reasons: []string{"job requires CI execution"}}
		if requirements := policy.Jobs[name]; requirements != nil {
			result, err := s.verifyRequirementsResult(initial, *requirements, nil)
			if err != nil {
				return CIPlan{}, fmt.Errorf("CI job %s: %w", name, err)
			}
			decision.Reasons = append([]string{}, result.RequirementFailures...)
			if result.Validated && result.Trusted && !result.External {
				decision.Action = "skip"
				decision.Reasons = []string{"matching trusted commit-bound execution contracts"}
			} else if len(decision.Reasons) == 0 {
				decision.Reasons = []string{"required trusted execution evidence does not match"}
			}
		}
		decision.VerificationMS = time.Since(started).Milliseconds()
		plan.Jobs = append(plan.Jobs, decision)
		if decision.Action == "skip" {
			plan.ReusableJobs = append(plan.ReusableJobs, name)
		} else {
			plan.RunJobs = append(plan.RunJobs, name)
		}
	}
	plan.VerificationMS = time.Since(start).Milliseconds()
	return plan, nil
}
