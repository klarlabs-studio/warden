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

// globMatch matches a path/branch glob against s. It supports:
//   - "**" — matches zero or more whole path segments (crosses "/")
//   - "*"  — matches any run of characters within a single segment
//   - "?"  — matches any single character within a segment
//
// Branch names without slashes are treated as single-segment paths, so plain
// patterns like "main" or "release/*" work unchanged.
func globMatch(pattern, s string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(s, "/"))
}

// matchSegments matches a slice of pattern segments against path segments,
// where a "**" pattern segment consumes zero or more path segments.
func matchSegments(pat, name []string) bool {
	// Empty pattern matches only empty name.
	if len(pat) == 0 {
		return len(name) == 0
	}

	if pat[0] == "**" {
		// Collapse consecutive "**".
		for len(pat) > 1 && pat[1] == "**" {
			pat = pat[1:]
		}
		// "**" as the final segment matches any remaining name (including none).
		if len(pat) == 1 {
			return true
		}
		// Try consuming 0..len(name) segments with "**".
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}

	// Non-"**" segment must match the next name segment.
	if len(name) == 0 {
		return false
	}
	if !segmentGlob(pat[0], name[0]) {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

// segmentGlob matches a single path segment with "*" and "?" wildcards that do
// not cross "/". Recursive backtracking on "*".
func segmentGlob(p, t string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			// Collapse consecutive "*".
			for len(p) > 1 && p[1] == '*' {
				p = p[1:]
			}
			if len(p) == 1 {
				return true // trailing star matches rest of segment
			}
			for i := 0; i <= len(t); i++ {
				if segmentGlob(p[1:], t[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(t) == 0 {
				return false
			}
			p, t = p[1:], t[1:]
		default:
			if len(t) == 0 || t[0] != p[0] {
				return false
			}
			p, t = p[1:], t[1:]
		}
	}
	return len(t) == 0
}
