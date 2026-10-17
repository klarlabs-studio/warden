package service

import (
	"fmt"
	"slices"

	"go.klarlabs.de/warden/internal/domain"
)

// VerifyRequirements narrows single-commit verification to fresh observations
// matching protected consumer expectations. It never widens Attests or trust.
func (s *Service) VerifyRequirements(commit string, policy ExternalPolicy, requirements domain.ExecutionRequirements, keys ...string) (VerifyResult, error) {
	if err := requirements.Validate(); err != nil {
		return VerifyResult{}, err
	}
	result, err := s.VerifyWithPolicy(commit, policy, keys...)
	if err != nil {
		return result, err
	}
	if !result.Validated || !result.Trusted || result.External || result.Record == nil {
		result.Validated = false
		result.RequirementFailures = []string{"trusted, commit-bound local provenance is required"}
		return result, nil
	}
	tree, err := s.repo.TreeSHA(result.SHA)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("resolve required commit tree: %w", err)
	}
	for _, step := range requirements.OrderedSteps() {
		contract, ok := result.Record.Contracts[step]
		reason := "missing execution contract (legacy, host, cached, skipped, or incomplete capture)"
		if ok && (!slices.Contains(result.Record.StepsRun, step) || result.Record.Environments[step] != contract.Environment) {
			reason = "execution contract does not match recorded step execution"
		} else if ok {
			reason = requirements.Steps[step].Mismatch(contract, tree)
		}
		if reason != "" {
			result.RequirementFailures = append(result.RequirementFailures, fmt.Sprintf("%s: %s", step, reason))
			result.Validated = false
		}
	}
	return result, nil
}
