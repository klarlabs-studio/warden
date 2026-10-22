package service

import (
	"fmt"
	"slices"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// scopeRequirementFailure is why a requirements check refuses a record of an
// affected-scope run.
const scopeRequirementFailure = "affected-scope: the note attests per-workspace checks for the affected workspaces only, and execution requirements need a full run"

// VerifyRequirements narrows single-commit verification to fresh observations
// matching protected consumer expectations. It never widens Attests or trust.
func (s *Service) VerifyRequirements(commit string, policy ExternalPolicy, requirements domain.ExecutionRequirements, keys ...string) (VerifyResult, error) {
	return s.VerifyRequirementsWithProfiles(commit, policy, requirements, nil, keys...)
}

// VerifyRequirementsWithProfiles is VerifyRequirements that also requires
// conformance to each profile in mins (from --min-profile), on top of any
// minimum a version 2 requirements file names. Every minimum must hold.
func (s *Service) VerifyRequirementsWithProfiles(commit string, policy ExternalPolicy, requirements domain.ExecutionRequirements, mins []domain.ComplianceProfile, keys ...string) (VerifyResult, error) {
	if err := requirements.Validate(); err != nil {
		return VerifyResult{}, err
	}
	result, err := s.VerifyWithPolicy(commit, policy, keys...)
	if err != nil {
		return result, err
	}
	return s.verifyRequirementsResult(result, requirements, mins)
}

// verifyRequirementsResult narrows one already verified snapshot. Keeping the
// snapshot fixed lets a CI plan evaluate jobs without refetching notes or
// mixing evidence from different runs. Requirements must be validated first.
func (s *Service) verifyRequirementsResult(result VerifyResult, requirements domain.ExecutionRequirements, mins []domain.ComplianceProfile) (VerifyResult, error) {
	result.RequirementFailures = slices.Clone(result.RequirementFailures)
	result.Conformance = slices.Clone(result.Conformance)
	// Version 1 requirements describe a full run. A record of an affected one
	// is refused here in its own right, whatever the verification policy: its
	// steps ran for some workspaces only, and its commands came from
	// per-workspace templates no contract describes.
	if result.Record != nil && (result.Affected || !result.Record.FullScope()) {
		result.Validated = false
		result.RequirementFailures = []string{scopeRequirementFailure}
		return result, nil
	}
	if !result.Validated || !result.Trusted || result.External || result.Record == nil {
		result.Validated = false
		result.RequirementFailures = []string{"trusted, commit-bound local provenance is required"}
		return result, nil
	}
	if p, ok := requirements.MinProfile(); ok {
		mins = append(slices.Clip(mins), p)
	}
	applyConformance(&result, mins)
	tree, err := s.repo.TreeSHA(result.SHA)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("resolve required commit tree: %w", err)
	}
	for _, step := range requirements.OrderedSteps() {
		contract, ok := result.Record.Contracts[step]
		reason := "missing execution contract (legacy, host, cached, skipped, or incomplete capture)"
		if ok && (!slices.Contains(result.Record.StepsRun, step) || result.Record.Environments[step] != contract.Environment) {
			reason = "execution contract does not match recorded step execution"
		} else if outcome, recorded := result.Record.StepOutcomes[step]; ok && recorded && outcome.Outcome != domain.StepExecuted {
			reason = "execution contract requires a fresh executed step"
		} else if ok {
			reason = requirements.Steps[step].Mismatch(contract, tree)
		}
		if reason == "" && requirements.Steps[step].ObservedLimits != nil {
			observed, present := result.Record.Observations[step]
			// Unprofiled runs omit StepOutcomes. Their fresh contract and probe
			// still bind the execution; an explicit reuse outcome contradicts them.
			outcome, recorded := result.Record.StepOutcomes[step]
			switch {
			case !present:
				reason = "missing initial container observation"
			case recorded && outcome.Outcome != domain.StepExecuted:
				reason = "initial observation requires a fresh executed step"
			default:
				reason = strings.Join(observed.Shortfalls(*requirements.Steps[step].ObservedLimits, "none"), "; ")
			}
		}
		if reason != "" {
			result.RequirementFailures = append(result.RequirementFailures, fmt.Sprintf("%s: %s", step, reason))
			result.Validated = false
		}
	}
	return result, nil
}
