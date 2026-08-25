package domain

import "fmt"

// BranchProtection is the forge's rule for the branch under report: what the
// forge REQUIRES, as distinct from what happened to occur.
//
// WHY EVIDENCE NEEDS IT. `--approvals` reports outcomes — twelve changes, twelve
// independent approvals. Two repositories produce that same line: one where
// review is required and enforced, and one where it merely happened and could
// be skipped silently tomorrow. The first has a control; the second has a habit.
// An auditor cannot tell them apart from outcomes alone, and warden's own CC8.1
// limits text already concedes the gap — "a change merged with administrator
// privileges past a required review appears as unapproved" is warden admitting
// it can see the outcome and not the rule.
//
// THE THREE STATES. `Known` is false when warden could not READ the rule, which
// is not the same as there being none. Reading branch protection needs admin
// rights on GitHub, so an ordinary token gets 403 — and reporting that as "this
// branch is unprotected" would be the defect this package exists to prevent,
// pointed at a control rather than a commit. A 404 IS an answer: the branch has
// no protection, and that is a finding worth stating.
type BranchProtection struct {
	// Known is false when warden could not determine the rule. Nothing else in
	// this struct is meaningful when it is false.
	Known bool `json:"known"`
	// Reason explains why the rule could not be read, for the operator.
	Reason string `json:"reason,omitempty"`
	// Protected is false when the forge answered that the branch has no
	// protection rule at all. Only meaningful when Known.
	Protected bool `json:"protected"`

	// RequiredApprovals is how many approving reviews the forge demands. Zero
	// with Protected true is a real and common configuration — it means pull
	// requests are required but reviews are not.
	RequiredApprovals int `json:"required_approvals"`
	// RequireConversationResolution demands every review thread be resolved
	// before merge. It is evidence-bearing: unresolved reviewer questions cannot
	// be left behind by a merge.
	RequireConversationResolution bool `json:"require_conversation_resolution"`
	// DismissStaleReviews drops approvals when new commits land, so an approval
	// always refers to the code that merged rather than an earlier revision.
	DismissStaleReviews bool `json:"dismiss_stale_reviews"`
	// RequireLastPushApproval stops the pusher's own approval counting for the
	// commits they just pushed.
	RequireLastPushApproval bool `json:"require_last_push_approval"`
	// EnforceAdmins decides whether any of the above binds a repository admin.
	// When false, every rule here is a default an admin may merge past — and
	// reporting the rules without saying so would overstate them.
	EnforceAdmins bool `json:"enforce_admins"`
}

// UnknownProtection records that the rule could not be read, and why.
func UnknownProtection(reason string) BranchProtection {
	return BranchProtection{Known: false, Reason: reason}
}

// RequiresIndependentReview reports whether the forge demands at least one
// approving review. It says nothing about whether that demand is enforceable —
// see Enforceable — because those are two separate facts and collapsing them
// would let "required" read as "unskippable".
func (b BranchProtection) RequiresIndependentReview() bool {
	return b.Known && b.Protected && b.RequiredApprovals > 0
}

// Enforceable reports whether the rules bind everyone, admins included.
func (b BranchProtection) Enforceable() bool {
	return b.Known && b.Protected && b.EnforceAdmins
}

// Summary is one line for a human report. It states the rule and, when the rule
// does not bind admins, says so in the same breath — a reader who takes away
// "review is required" without "unless an admin merges" has been misled by an
// accurate sentence.
func (b BranchProtection) Summary() string {
	switch {
	case !b.Known:
		return "not determined — " + b.Reason
	case !b.Protected:
		return "no branch protection rule: nothing prevents a direct push or an unreviewed merge"
	}
	var s string
	if b.RequiredApprovals > 0 {
		s = fmt.Sprintf("%d approving review(s) required", b.RequiredApprovals)
	} else {
		s = "pull request required, but no approving review is"
	}
	if b.DismissStaleReviews {
		s += "; stale approvals dismissed on new commits"
	}
	if b.RequireLastPushApproval {
		s += "; the last pusher's own approval does not count"
	}
	if b.RequireConversationResolution {
		s += "; all review threads must be resolved"
	}
	if !b.EnforceAdmins {
		s += ". NOT enforced against administrators, so each rule above is a default an admin may merge past"
	}
	return s
}
