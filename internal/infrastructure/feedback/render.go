package feedback

import (
	"fmt"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// The renderers turn a document the reader has already judged (Freshness
// filled in) into formats editors and tools consume. They follow two rules:
//
//   - A location is printed only when the document carries one. warden does
//     not guess a file or line from a tool's output.
//   - Results are shown as diagnostics only while they describe the worktree
//     as it is now. A document for another snapshot, another environment, or
//     from a watcher that died is reported as such, and its findings are
//     withheld rather than shown as if they were current.

// Diagnosable reports whether the document's results may be shown as current
// diagnostics, and if not, why. expired concerns only passing steps whose
// prediction aged out; the failures it carries still describe this snapshot.
func Diagnosable(doc domain.FeedbackDocument) (show bool, why string) {
	switch doc.Freshness {
	case domain.FreshnessCurrent:
		return true, ""
	case domain.FreshnessExpired:
		return true, "a passing step's staged prediction has expired, so a commit would run it again"
	case domain.FreshnessStaleSnapshot:
		return false, "the staged tree, HEAD, branch or configuration changed since these results were observed; diagnostics are withheld until the watcher checks the worktree again"
	case domain.FreshnessStaleEnvironment:
		return false, "the container environment changed since these results were observed; diagnostics are withheld until the watcher checks again"
	case domain.FreshnessProducerGone:
		return false, "the watcher that wrote these results stopped without finishing; diagnostics are withheld until `warden watch --staged` runs again"
	default:
		return false, fmt.Sprintf("freshness %q was not computed; diagnostics are withheld", doc.Freshness)
	}
}

// located reports whether a finding names a file and a line.
func located(f domain.FeedbackFinding) bool { return f.File != "" && f.Line > 0 }

// blocked reports whether a step ended because of the machine, not the change.
func blocked(s *domain.FeedbackStep) bool { return s.Blocker != domain.BlockerNone }

// statusCounts summarizes the steps in a fixed order, naming only statuses
// that occur.
func statusCounts(steps []domain.FeedbackStep) string {
	order := []struct {
		status domain.FeedbackStepStatus
		label  string
	}{
		{domain.FeedbackStepFail, "failed"},
		{domain.FeedbackStepRunning, "running"},
		{domain.FeedbackStepPending, "pending"},
		{domain.FeedbackStepPass, "passed"},
		{domain.FeedbackStepCached, "cached"},
		{domain.FeedbackStepSkipped, "skipped"},
	}
	counts := map[domain.FeedbackStepStatus]int{}
	blockedCount := 0
	for i := range steps {
		if steps[i].Status == domain.FeedbackStepFail && blocked(&steps[i]) {
			blockedCount++
			continue
		}
		counts[steps[i].Status]++
	}
	var parts []string
	if blockedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blockedCount))
	}
	for _, o := range order {
		if n := counts[o.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, o.label))
		}
	}
	if len(parts) == 0 {
		return "no steps"
	}
	return strings.Join(parts, ", ")
}

// oneLine reduces text to its first non-empty line, so a tool's multi-line
// output cannot forge further diagnostic lines in a line-oriented format.
func oneLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(strings.TrimRight(line, "\r")); line != "" {
			return strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f {
					return ' '
				}
				return r
			}, line)
		}
	}
	return ""
}
