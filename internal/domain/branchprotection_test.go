package domain

import (
	"strings"
	"testing"
)

// The distinction the whole feature exists for: warden could not READ the rule
// is not the same as there is no rule. Reading branch protection needs admin
// rights, so an ordinary token gets 403 — and an evidence document that turned
// that into "this branch is unprotected" would be accusing a repository of a
// control gap on the strength of a permission warden does not hold.
func TestBranchProtection_UnknownIsNotUnprotected(t *testing.T) {
	unknown := UnknownProtection("gh: Must have admin rights to Repository. (HTTP 403)")
	unprotected := BranchProtection{Known: true, Protected: false}

	if unknown.RequiresIndependentReview() || unknown.Enforceable() {
		t.Error("an unreadable rule must assert nothing")
	}
	if !strings.Contains(unknown.Summary(), "not determined") {
		t.Errorf("unknown summary must say so, got %q", unknown.Summary())
	}
	if strings.Contains(unknown.Summary(), "no branch protection") {
		t.Errorf("unknown must not read as unprotected: %q", unknown.Summary())
	}
	// And the genuine 404 answer must still be reportable as the finding it is.
	if !strings.Contains(unprotected.Summary(), "no branch protection rule") {
		t.Errorf("an answered 'no rule' must say so, got %q", unprotected.Summary())
	}
}

// "Required" and "unskippable" are two facts. A rule that does not bind admins
// is a default, and a summary that stated the requirement without that caveat
// would be accurate and still mislead.
func TestBranchProtection_RequiredIsNotEnforcedUnlessAdminsAreBound(t *testing.T) {
	required := BranchProtection{Known: true, Protected: true, RequiredApprovals: 2}
	if !required.RequiresIndependentReview() {
		t.Error("2 required approvals must count as requiring review")
	}
	if required.Enforceable() {
		t.Error("enforce_admins is false — the rule is not enforceable against everyone")
	}
	if !strings.Contains(required.Summary(), "NOT enforced against administrators") {
		t.Errorf("summary must carry the admin caveat, got %q", required.Summary())
	}

	bound := required
	bound.EnforceAdmins = true
	if !bound.Enforceable() {
		t.Error("with enforce_admins the rule binds everyone")
	}
	if strings.Contains(bound.Summary(), "NOT enforced") {
		t.Errorf("an enforced rule must not carry the caveat, got %q", bound.Summary())
	}
}

// Zero required approvals with protection ON is a real, common configuration —
// warden's own repository is exactly this. It must not be conflated with either
// "review required" or "no protection".
func TestBranchProtection_PRRequiredButNoReviewRequired(t *testing.T) {
	p := BranchProtection{Known: true, Protected: true, RequiredApprovals: 0, EnforceAdmins: true}
	if p.RequiresIndependentReview() {
		t.Error("zero required approvals must not read as requiring review")
	}
	got := p.Summary()
	if !strings.Contains(got, "pull request required, but no approving review is") {
		t.Errorf("summary should distinguish PR-required from review-required, got %q", got)
	}
}

// The control text must change with the rule, or the feature is decoration.
// Four distinct worlds, four distinct sentences.
func TestWithProtection_ControlTextReflectsTheRule(t *testing.T) {
	base := Evidence{
		Branch:   "main",
		Controls: []Control{{Framework: "SOC 2", ID: "CC8.1", Evidences: "base.", Limits: "Approval is evidenced only as strongly as the forge records it. Authorization — that the change was wanted — is still a separate control with separate evidence."}},
	}
	approvals := map[string]Approval{"a": {Found: true, PR: 1, Author: "x"}}

	cases := []struct {
		name string
		prot BranchProtection
		want string
	}{
		{"enforced requirement", BranchProtection{Known: true, Protected: true, RequiredApprovals: 1, EnforceAdmins: true},
			"a control rather than a convention"},
		{"unenforced requirement", BranchProtection{Known: true, Protected: true, RequiredApprovals: 1},
			"does NOT enforce that against administrators"},
		{"pr but no review", BranchProtection{Known: true, Protected: true},
			"does NOT require an approving review"},
		{"no protection at all", BranchProtection{Known: true, Protected: false},
			"NO forge protection rule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := base.WithProtection(approvals, c.prot).Controls[0].Evidences
			if !strings.Contains(got, c.want) {
				t.Errorf("Evidences = %q\n  missing %q", got, c.want)
			}
		})
	}
}

// The new claim's own limit. Forges expose no history of protection settings,
// so this reports the rule in force NOW — and a report that let a reader assume
// otherwise would be a fresh over-claim in the surface built to avoid them.
func TestWithProtection_SaysTheRuleIsReadAtReportTime(t *testing.T) {
	base := Evidence{Branch: "main", Controls: []Control{{Framework: "SOC 2", ID: "CC8.1", Limits: "Authorization — that the change was wanted — is still a separate control with separate evidence."}}}
	known := base.WithProtection(map[string]Approval{"a": {Found: true}},
		BranchProtection{Known: true, Protected: true, RequiredApprovals: 1})
	if !strings.Contains(known.Controls[0].Limits, "WHEN THIS REPORT WAS PRODUCED") {
		t.Errorf("limits must date the rule, got %q", known.Controls[0].Limits)
	}

	// And when it could not be read, the report must say that rather than imply
	// review was not required.
	unknown := base.WithProtection(map[string]Approval{"a": {Found: true}},
		UnknownProtection("HTTP 403"))
	lim := unknown.Controls[0].Limits
	if !strings.Contains(lim, "could not be read") || !strings.Contains(lim, "not the same as saying it was not") {
		t.Errorf("unreadable rule must be disclaimed, got %q", lim)
	}
}
