package forge

import (
	"context"
	"encoding/json"

	"go.klarlabs.de/warden/internal/domain"
)

// ProtectionFor reads the forge's protection rule for a branch.
//
// The status codes carry the whole meaning here, which is why this reads them
// rather than the exit code:
//
//	200  the rule, whatever it says
//	404  the forge answered: this branch has NO protection rule. A finding.
//	403  the token cannot see protection (GitHub requires admin). NOT an answer.
//	     Anything else, likewise.
//
// Folding 403 into 404 would report "this branch is unprotected" to an auditor
// on the strength of a permission warden does not hold — the same substitution
// of "I could not look" for "there is nothing there" that this codebase has now
// fixed in four other places.
func (g *GH) ProtectionFor(ctx context.Context, branch string) domain.BranchProtection {
	out, errOut, err := g.runSplit(ctx, "api", "--include",
		"repos/{owner}/{repo}/branches/"+branch+"/protection")

	status, body := splitHTTPResponse(out)
	switch {
	case status == 404:
		// Answered, and the answer is "no rule".
		return domain.BranchProtection{Known: true, Protected: false}
	case status != 200:
		return domain.UnknownProtection(cause(errOut, err))
	}

	var raw struct {
		RequiredPullRequestReviews *struct {
			RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
			DismissStaleReviews          bool `json:"dismiss_stale_reviews"`
			RequireLastPushApproval      bool `json:"require_last_push_approval"`
		} `json:"required_pull_request_reviews"`
		RequiredConversationResolution *struct {
			Enabled bool `json:"enabled"`
		} `json:"required_conversation_resolution"`
		EnforceAdmins *struct {
			Enabled bool `json:"enabled"`
		} `json:"enforce_admins"`
	}
	if json.Unmarshal([]byte(body), &raw) != nil {
		return domain.UnknownProtection("the forge's reply could not be parsed")
	}

	p := domain.BranchProtection{Known: true, Protected: true}
	// Each block is absent rather than false when the setting is off, so a nil
	// check is the difference between "not configured" and "configured off".
	// They mean the same thing to a reader here, and neither may be guessed at.
	if r := raw.RequiredPullRequestReviews; r != nil {
		p.RequiredApprovals = r.RequiredApprovingReviewCount
		p.DismissStaleReviews = r.DismissStaleReviews
		p.RequireLastPushApproval = r.RequireLastPushApproval
	}
	if c := raw.RequiredConversationResolution; c != nil {
		p.RequireConversationResolution = c.Enabled
	}
	if a := raw.EnforceAdmins; a != nil {
		p.EnforceAdmins = a.Enabled
	}
	return p
}
