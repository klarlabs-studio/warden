package application

import (
	"fmt"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// commentMarker is a hidden HTML marker on the gate comment so the sticky-update
// path (and a human) can recognize warden's comment.
const commentMarker = "<!-- warden-gate -->"

// prComment renders the gate-result PR comment for a passing push: the steps
// run, the provenance record (signed by whom), and any findings. The leading
// marker keeps the comment identifiable and sticky.
func prComment(res RunResult, branch string) string {
	var b strings.Builder
	b.WriteString(commentMarker + "\n")
	b.WriteString("### ✅ Warden gate passed\n\n")

	// "after running" counts only steps that executed. A step that passed
	// without executing (cached, or skipped for want of an agent) is listed
	// separately with why: a reviewer reading `review` in the ran-list would
	// assume a review was performed.
	var ran, notRan []domain.StepName
	for _, s := range res.Policy.Steps {
		if _, ok := res.StepNotes[s]; ok {
			notRan = append(notRan, s)
		} else {
			ran = append(ran, s)
		}
	}
	fmt.Fprintf(&b, "Pushed `%s` after running **%d step%s**", branch, len(ran), plural(len(ran)))
	if len(ran) > 0 {
		b.WriteString(": " + codeList(ran))
	}
	b.WriteString(".\n\n")
	if len(notRan) > 0 {
		b.WriteString("Passed without running:\n")
		for _, s := range notRan {
			n := res.StepNotes[s]
			fmt.Fprintf(&b, "- `%s`: **%s**", s, n.Outcome)
			if n.Reason != "" {
				b.WriteString(" — " + n.Reason)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if rec := res.Record; rec != nil {
		fmt.Fprintf(&b, "**Provenance:** `%s` · chain-intact", rec.RunID)
		if rec.Signed() {
			fmt.Fprintf(&b, " · signed by `%s`", rec.SignerFingerprint())
		} else {
			b.WriteString(" · unsigned")
		}
		b.WriteString("\n\n")
		b.WriteString(scopeComment(rec))
	}

	if len(res.Findings) == 0 {
		b.WriteString("No findings. 🎉\n")
	} else {
		fmt.Fprintf(&b, "**Findings (%d):**\n", len(res.Findings))
		for _, f := range res.Findings {
			b.WriteString("- " + finding(f) + "\n")
		}
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// codeList renders step names as a comma-separated list of inline code spans.
func codeList(steps []domain.StepName) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = "`" + string(s) + "`"
	}
	return strings.Join(parts, ", ")
}

// finding renders one finding as a markdown bullet body.
func finding(f domain.Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	if loc != "" {
		return fmt.Sprintf("**[%s]** ", f.Severity) + "`" + loc + "` — " + f.Message
	}
	return fmt.Sprintf("**[%s]** %s", f.Severity, f.Message)
}

// maxScopeNames bounds how many workspace paths the comment spells out.
const maxScopeNames = 10

// scopeComment says plainly which workspaces the per-workspace steps checked,
// when the note records a scope, and "" otherwise. An affected scope is named
// as the partial check it is, with the full-scope run it rests on and how
// verifiers treat it, so nobody reads a green comment as a full gate.
func scopeComment(rec *domain.RunRecord) string {
	scope := rec.Scope
	if scope == nil {
		return ""
	}
	n, m := scope.Proportion()
	if rec.FullScope() {
		return fmt.Sprintf("**Scope:** full — per-workspace steps ran for every workspace (%d).\n\n", m)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Scope:** affected — per-workspace steps ran for **%d of %d** workspaces", n, m)
	if n > 0 {
		names := make([]string, 0, maxScopeNames)
		for i, w := range scope.Selected {
			if i == maxScopeNames {
				names = append(names, fmt.Sprintf("and %d more", n-maxScopeNames))
				break
			}
			names = append(names, "`"+w.Path+"`")
		}
		b.WriteString(" (" + strings.Join(names, ", ") + ")")
	}
	if scope.Anchor != "" {
		anchor := scope.Anchor
		if len(anchor) > 12 {
			anchor = anchor[:12]
		}
		fmt.Fprintf(&b, "; the rest rest on the full-scope run on `%s`", anchor)
	}
	b.WriteString(". This is a partial check: `warden verify` refuses an affected-scope note by default.\n\n")
	return b.String()
}
