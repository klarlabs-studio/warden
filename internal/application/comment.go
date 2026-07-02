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

	steps := res.Policy.Steps
	fmt.Fprintf(&b, "Pushed `%s` after running **%d step%s**", branch, len(steps), plural(len(steps)))
	if len(steps) > 0 {
		b.WriteString(": " + codeList(steps))
	}
	b.WriteString(".\n\n")

	if rec := res.Record; rec != nil {
		fmt.Fprintf(&b, "**Provenance:** `%s` · chain-intact", rec.RunID)
		if rec.Signed() {
			fmt.Fprintf(&b, " · signed by `%s`", rec.SignerFingerprint())
		} else {
			b.WriteString(" · unsigned")
		}
		b.WriteString("\n\n")
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
		return fmt.Sprintf("**[%s]** `%s` — %s", f.Severity, loc, f.Message)
	}
	return fmt.Sprintf("**[%s]** %s", f.Severity, f.Message)
}
