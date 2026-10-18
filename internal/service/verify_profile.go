package service

import (
	"fmt"
	"slices"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// ConformanceReport is the result of checking one note against a minimum
// compliance profile (ADR 0007, L3).
//
// It separates what was checked from what was not. A clause the signed record
// cannot yet support is listed in Deferred and never in Checked, so a passing
// report cannot be read as covering a guarantee nobody looked at.
type ConformanceReport struct {
	// Min is the profile demanded, as name@version.
	Min string
	// Checked names every clause that was evaluated against the record.
	Checked []string
	// Deferred names clauses of Min that this release does not check because
	// the signed record does not carry the observations they need.
	Deferred []string
	// Failures lists each clause that did not hold, with the reason.
	Failures []string
}

// OK reports whether the note conforms: every checked clause held.
func (r ConformanceReport) OK() bool { return len(r.Failures) == 0 }

// Conformance clause names. They are part of verify's output, so they are
// phrased as what was established, not as internal identifiers.
const (
	clauseTrusted   = "trusted, commit-bound local provenance"
	clauseDigest    = "recorded profile matches this warden's built-in definition"
	clauseRank      = "recorded profile ranks at least %s"
	clauseSteps     = "required steps %s %s"
	clauseContainer = "required shell steps %s ran in a recorded Linux container"
	clauseNetwork   = "required shell steps %s ran with network none"
	clauseApproval  = "a required approval was answered by a terminal or the TUI, not auto-approved"

	// Clauses strict@1 defines that the signed record cannot support yet.
	deferredIsolation  = "agent steps ran isolated with the canonical tree unchanged (not in the signed record yet)"
	deferredAuthorship = "a declared agent commit carries a notarized Agent Trace (authorship is not in the signed record yet)"
)

// Conformance decides whether a verified note meets the profile minimum.
//
// It is recomputed from the record's signed OBSERVATIONS, never from the
// profile name the record carries: a name is a claim, and a note that says
// "strict" proves nothing about what ran. The recorded profile only has to be
// a known built-in definition, byte-identical to this warden's, ranked at least
// minimum. Every other clause is read from step outcomes, execution environments
// and the approval record.
//
// res must come from VerifyWithPolicy with pinned keys: Validated, Trusted and
// not External are the first clause, and nothing else is evaluated without
// them, because an untrusted record's observations are not evidence.
//
// Only the pre-push hook writes notes, so "required steps" means minimum's
// required pre-push steps.
func Conformance(minimum domain.ComplianceProfile, res VerifyResult) ConformanceReport {
	rep := ConformanceReport{Min: minimum.String(), Checked: []string{clauseTrusted}}
	if minimum.Level() >= domain.ProfileStrict {
		rep.Deferred = []string{deferredIsolation, deferredAuthorship}
	}
	rec := res.Record
	if !res.Validated || !res.Trusted || res.External || rec == nil {
		rep.fail(clauseTrusted, "the note is missing, unbound, untrusted or external")
		return rep
	}

	rep.Checked = append(rep.Checked, clauseDigest, fmt.Sprintf(clauseRank, minimum))
	if recorded, ok := recordedProfile(&rep, rec.Profile); ok && recorded.Lowers(minimum) {
		rep.fail(fmt.Sprintf(clauseRank, minimum), fmt.Sprintf("the note was gated under %s", recorded))
	}

	checkStepOutcomes(&rep, minimum, rec)
	if minimum.RequiresContainer() || minimum.RequiresNetworkNone() {
		checkEnvironments(&rep, minimum, rec)
	}
	// The definitions that withhold approval from the agent surface are the
	// ones whose notes must show a required approval was not auto-approved.
	if !minimum.AgentSurfaceMayApprove() {
		checkApproval(&rep, rec.Approval)
	}
	return rep
}

func (r *ConformanceReport) fail(clause, reason string) {
	r.Failures = append(r.Failures, clause+": "+reason)
}

// recordedProfile resolves the record's profile against this warden's table.
// An absent profile, an unknown version, or a digest that differs from the
// built-in definition fails closed: the verifier cannot say what such a
// profile meant, so it cannot rank it.
func recordedProfile(rep *ConformanceReport, ref *domain.ProfileRef) (domain.ComplianceProfile, bool) {
	if ref == nil {
		rep.fail(clauseDigest, "the note records no profile")
		return domain.ComplianceProfile{}, false
	}
	p, ok := domain.LookupComplianceProfile(ref.Name, ref.Version)
	if !ok {
		rep.fail(clauseDigest, fmt.Sprintf("%s is not a profile this warden knows", ref))
		return domain.ComplianceProfile{}, false
	}
	if ref.Digest != p.Digest() {
		rep.fail(clauseDigest, fmt.Sprintf("the note's %s digest differs from this warden's definition", ref))
		return domain.ComplianceProfile{}, false
	}
	return p, true
}

// acceptedOutcomes is what a required step may have recorded to count. Under
// strict only a step that executed now counts (ADR 0007 Q14): a host cache hit
// or a staged prediction rests on an earlier run the note does not describe.
// Below strict those reuse a pass recorded for identical inputs and count too.
// A skipped step never counts: nothing performed it.
func acceptedOutcomes(minimum domain.ComplianceProfile) []domain.StepOutcome {
	if minimum.Level() >= domain.ProfileStrict {
		return []domain.StepOutcome{domain.StepExecuted}
	}
	return []domain.StepOutcome{domain.StepExecuted, domain.StepCached, domain.StepPredicted}
}

func checkStepOutcomes(rep *ConformanceReport, minimum domain.ComplianceProfile, rec *domain.RunRecord) {
	required := minimum.RequiredSteps(domain.PrePush)
	if len(required) == 0 {
		return
	}
	accepted := acceptedOutcomes(minimum)
	names := make([]string, len(accepted))
	for i, o := range accepted {
		names[i] = string(o)
	}
	clause := fmt.Sprintf(clauseSteps, domain.JoinSteps(required), strings.Join(names, " or "))
	rep.Checked = append(rep.Checked, clause)
	for _, s := range required {
		obs, ok := rec.StepOutcomes[s]
		switch {
		case !ok:
			rep.fail(clause, fmt.Sprintf("%s has no recorded outcome", s))
		case !slices.Contains(accepted, obs.Outcome):
			rep.fail(clause, fmt.Sprintf("%s was %s", s, obs.Outcome))
		}
	}
}

// checkEnvironments enforces the container and network clauses per required
// shell step. The network is checked here explicitly: ContainerChecks accepts
// any recorded network, including bridge (finding F8).
func checkEnvironments(rep *ConformanceReport, minimum domain.ComplianceProfile, rec *domain.RunRecord) {
	steps := minimum.RequiredShellSteps(domain.PrePush)
	if len(steps) == 0 {
		return
	}
	list := domain.JoinSteps(steps)
	if minimum.RequiresContainer() {
		clause := fmt.Sprintf(clauseContainer, list)
		rep.Checked = append(rep.Checked, clause)
		for _, s := range steps {
			if !rec.ContainerChecks([]domain.StepName{s}, "") {
				rep.fail(clause, fmt.Sprintf("%s has no complete container execution record", s))
			}
		}
	}
	if minimum.RequiresNetworkNone() {
		clause := fmt.Sprintf(clauseNetwork, list)
		rep.Checked = append(rep.Checked, clause)
		for _, s := range steps {
			env, ok := rec.Environments[s]
			switch {
			case !ok:
				rep.fail(clause, fmt.Sprintf("%s has no recorded execution environment", s))
			case env.Network != "none":
				rep.fail(clause, fmt.Sprintf("%s ran with network %q", s, env.Network))
			}
		}
	}
}

// checkApproval refuses an auto-approved gate wherever a decision was
// required. A record that is internally inconsistent (no decision required but
// a channel named, or an unknown channel) fails rather than being interpreted.
func checkApproval(rep *ConformanceReport, a *domain.ApprovalRef) {
	rep.Checked = append(rep.Checked, clauseApproval)
	switch {
	case a == nil:
		rep.fail(clauseApproval, "the note records no approval gate")
	case !a.Required && a.Channel != domain.ApprovalNone:
		rep.fail(clauseApproval, fmt.Sprintf("no decision was required, yet channel %q is recorded", a.Channel))
	case a.Required && a.Channel == domain.ApprovalAuto:
		rep.fail(clauseApproval, "the required approval was answered automatically")
	case a.Required && a.Channel != domain.ApprovalTTY && a.Channel != domain.ApprovalTUI:
		rep.fail(clauseApproval, fmt.Sprintf("the required approval was answered by unknown channel %q", a.Channel))
	}
}

// VerifyMinProfile verifies one commit and then requires it to conform to every
// profile in mins. It never widens VerifyWithPolicy: a note that fails there
// fails here, and a conformance failure only ever turns Validated off.
func (s *Service) VerifyMinProfile(commit string, policy ExternalPolicy, mins []domain.ComplianceProfile, keys ...string) (VerifyResult, error) {
	res, err := s.VerifyWithPolicy(commit, policy, keys...)
	if err != nil {
		return res, err
	}
	applyConformance(&res, mins)
	return res, nil
}

// applyConformance evaluates each minimum against the base verification result
// and folds the failures into it. It must run before any other consumer
// requirement turns Validated off, so the trust clause reads the verification
// itself.
func applyConformance(res *VerifyResult, mins []domain.ComplianceProfile) {
	base := *res
	for _, minimum := range mins {
		rep := Conformance(minimum, base)
		res.Conformance = append(res.Conformance, rep)
		for _, f := range rep.Failures {
			res.RequirementFailures = append(res.RequirementFailures, "profile "+rep.Min+": "+f)
			res.Validated = false
		}
	}
}
