package forge

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

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
// Three outcomes, and they must stay three:
//
//   - a pull request was found (Found)
//   - the forge answered, and there is no pull request for this commit
//     (the zero Approval) — a real finding
//   - warden could not get an answer (Undetermined) — NOT a finding, and the
//     reason this function reads the HTTP status rather than the exit code
//
// Folding the third into the second is how an evidence document ends up
// asserting that every change bypassed review when what actually happened is
// that a token expired.
func (g *GH) ApprovalFor(ctx context.Context, sha string) (domain.Approval, error) {
	// --include so the response status is readable. Without it a broken
	// credential and a commit the forge genuinely has no pull request for are
	// both just "gh exited non-zero".
	out, errOut, err := g.runSplit(ctx, "api", "--include",
		"repos/{owner}/{repo}/commits/"+sha+"/pulls",
		"--jq", "[.[]|{number, author: .user.login}]")

	status, body := splitHTTPResponse(out)
	switch {
	case status == 0:
		// gh never reached the API — not installed for this repo, network down,
		// context cancelled. Nothing was observed.
		return undetermined(cause(errOut, err)), nil
	case status == 404 || status == 422:
		// The forge answered authoritatively that it has no such commit (422) or
		// no such path (404). Reachable() has already established that the
		// repository itself is readable, so this is about the commit: it never
		// reached the forge, and so cannot have arrived through a pull request.
		// That is a finding, and must not be diluted into "could not tell".
		return domain.Approval{}, nil
	case status != 200:
		// 401/403 (credential), 429 (rate limit), 5xx (the forge). All of these
		// are warden failing to look.
		return undetermined(cause(errOut, err)), nil
	}

	var prs []struct {
		Number int    `json:"number"`
		Author string `json:"author"`
	}
	if json.Unmarshal([]byte(body), &prs) != nil {
		// A 200 whose body will not parse is not an answer either.
		return undetermined("the forge's reply could not be parsed"), nil
	}
	if len(prs) == 0 {
		return domain.Approval{}, nil // answered, and there is no pull request
	}
	// The first is the pull request the commit was merged through; later ones
	// are back-ports and forks that also contain it.
	pr := prs[0]

	reviews, revErrOut, revErr := g.runSplit(ctx, "api", "--include",
		"repos/{owner}/{repo}/pulls/"+strconv.Itoa(pr.Number)+"/reviews",
		"--jq", "[.[]|select(.state==\"APPROVED\")|.user.login]")
	approval := domain.Approval{Found: true, PR: pr.Number, Author: pr.Author}

	revStatus, revBody := splitHTTPResponse(reviews)
	if revStatus != 200 {
		// The pull request is known; who approved it is not. Reporting an empty
		// approver list would assert "nobody approved this", which is precisely
		// the claim warden did not observe.
		approval.Undetermined = true
		approval.Reason = "pull request #" + strconv.Itoa(pr.Number) + ": " + cause(revErrOut, revErr)
		return approval, nil
	}
	var approvers []string
	if json.Unmarshal([]byte(revBody), &approvers) != nil {
		approval.Undetermined = true
		approval.Reason = "pull request #" + strconv.Itoa(pr.Number) + ": the forge's reply could not be parsed"
		return approval, nil
	}
	approval.Approvers = dedupe(approvers)
	return approval, nil
}

func undetermined(reason string) domain.Approval {
	return domain.Approval{Undetermined: true, Reason: reason}
}

// cause prefers gh's own diagnosis over the exec error, which is only ever
// "exit status 1".
func cause(stderr string, err error) string {
	if c := firstLine(stderr); c != "" {
		return c
	}
	if err != nil {
		return err.Error()
	}
	return "the forge did not answer"
}

// splitHTTPResponse reads `gh api --include` output: a status line, headers, a
// blank line, then the body (jq-filtered, when --jq was given). It returns 0
// when there is no status line at all, which is how "gh never got a reply"
// reaches the caller.
func splitHTTPResponse(out string) (status int, body string) {
	rest := out
	// Skip anything gh printed before the status line (it prints none today;
	// tolerating it costs nothing and avoids a parse that fails on a banner).
	idx := strings.Index(rest, "HTTP/")
	if idx < 0 {
		return 0, ""
	}
	rest = rest[idx:]

	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return 0, ""
	}
	statusLine := strings.TrimSpace(rest[:nl])
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0, ""
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, ""
	}

	// Headers end at the first blank line. Normalise CRLF so a header block
	// that arrives with \r\n does not leave a stray \r in front of the body.
	rest = strings.ReplaceAll(rest[nl+1:], "\r\n", "\n")
	if i := strings.Index(rest, "\n\n"); i >= 0 {
		body = rest[i+2:]
	}
	return code, strings.TrimSpace(body)
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
