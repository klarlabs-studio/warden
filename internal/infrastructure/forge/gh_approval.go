package forge

import (
	"context"
	"encoding/json"

	"go.klarlabs.de/warden/internal/domain"
)

// ApprovalFor reads the forge's review record for a commit: the pull request it
// arrived through, who opened it, and who approved it.
//
// warden observes the gate, not the review, so this is the one part of change
// management it can only report second-hand. It is therefore read verbatim and
// interpreted in the domain — a self-approval stays visible as a self-approval
// rather than being folded into an approval count here.
//
// A commit with no associated pull request is not an error: it was pushed to
// the branch directly, which is a finding, not a failure to look.
func (g *GH) ApprovalFor(ctx context.Context, sha string) (domain.Approval, error) {
	out, err := g.run(ctx, "api", "repos/{owner}/{repo}/commits/"+sha+"/pulls",
		"--jq", "[.[]|{number, author: .user.login}]")
	if err != nil {
		// gh exits non-zero for a commit the forge does not have, which is the
		// same shape as "no pull request" for our purposes: nothing to report.
		return domain.Approval{}, nil
	}
	var prs []struct {
		Number int    `json:"number"`
		Author string `json:"author"`
	}
	if json.Unmarshal([]byte(out), &prs) != nil || len(prs) == 0 {
		return domain.Approval{}, nil
	}
	// The first is the pull request the commit was merged through; later ones
	// are back-ports and forks that also contain it.
	pr := prs[0]

	reviews, err := g.run(ctx, "api", "repos/{owner}/{repo}/pulls/"+itoa(pr.Number)+"/reviews",
		"--jq", "[.[]|select(.state==\"APPROVED\")|.user.login]")
	approval := domain.Approval{Found: true, PR: pr.Number, Author: pr.Author}
	if err != nil {
		return approval, nil // the PR is known; its reviews are not
	}
	var approvers []string
	if json.Unmarshal([]byte(reviews), &approvers) == nil {
		approval.Approvers = dedupe(approvers)
	}
	return approval, nil
}

// dedupe keeps one entry per approver: GitHub records a review per submission,
// so one person approving twice must not read as two people.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
