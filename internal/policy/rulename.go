package policy

import (
	"fmt"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// ruleName derives a stable name for provenance. It prefers a human-readable
// tag built from the match, falling back to a positional name.
func ruleName(r domain.Rule, index int) string {
	var parts []string
	if r.Match.Branch != "" {
		parts = append(parts, "branch="+r.Match.Branch)
	}
	if r.Match.Risk != "" {
		parts = append(parts, "risk="+string(r.Match.Risk))
	}
	if len(r.Match.Paths) > 0 {
		parts = append(parts, "paths="+strings.Join(r.Match.Paths, ","))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("rule[%d]", index)
	}
	return strings.Join(parts, " ")
}
