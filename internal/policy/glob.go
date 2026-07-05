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

// Defense-in-depth caps. The linear matchers below are already O(n·m), so
// these do not gate any legitimate glob — they merely bound absurd, hostile
// input (patterns arrive from .warden.yaml, which may be pulled in untrusted
// via `extends:`). The limits sit orders of magnitude above any real path or
// branch glob, so existing inputs are unaffected.
const (
	maxGlobLen      = 4096 // max bytes in a pattern or a matched string
	maxGlobSegments = 512  // max "/"-separated segments in either side
)

// globMatch matches a path/branch glob against s. It supports:
//   - "**" — matches zero or more whole path segments (crosses "/")
//   - "*"  — matches any run of characters within a single segment
//   - "?"  — matches any single character within a segment
//
// Branch names without slashes are treated as single-segment paths, so plain
// patterns like "main" or "release/*" work unchanged.
//
// Matching is linear (O(n·m)) via two-pointer greedy wildcard matching with a
// single backtrack memo, applied both across segments (for "**") and within a
// segment (for "*"/"?"). It never backtracks exponentially, so hostile patterns
// such as "*a*a*a*a*b" or "a/**/b/**/b/**/…" cannot hang the gate.
func globMatch(pattern, s string) bool {
	// Reject pathologically large input outright (defense-in-depth).
	if len(pattern) > maxGlobLen || len(s) > maxGlobLen {
		return false
	}
	pat := strings.Split(pattern, "/")
	name := strings.Split(s, "/")
	if len(pat) > maxGlobSegments || len(name) > maxGlobSegments {
		return false
	}
	return matchSegments(pat, name)
}

// matchSegments matches pattern segments against path segments, where a "**"
// pattern segment consumes zero or more whole path segments.
//
// This is the classic two-pointer greedy wildcard matcher lifted to the segment
// level: "**" plays the role of "*", and a literal (non-"**") segment matches a
// name segment via segmentGlob. A single remembered backtrack position keeps it
// O(len(pat)·len(name)) — no recursion, no exponential blowup. Consecutive "**"
// collapse naturally (each just re-arms the same backtrack point).
func matchSegments(pat, name []string) bool {
	var (
		pi, ni    int
		star      = -1 // index in pat of the most recent "**", or -1
		starMatch int  // name index that "**" is currently pinned at
	)
	for ni < len(name) {
		switch {
		case pi < len(pat) && pat[pi] != "**" && segmentGlob(pat[pi], name[ni]):
			pi++
			ni++
		case pi < len(pat) && pat[pi] == "**":
			star = pi
			starMatch = ni
			pi++
		case star != -1:
			// Backtrack: let the last "**" swallow one more name segment.
			pi = star + 1
			starMatch++
			ni = starMatch
		default:
			return false
		}
	}
	// Any trailing "**" segments match zero remaining name segments.
	for pi < len(pat) && pat[pi] == "**" {
		pi++
	}
	return pi == len(pat)
}

// segmentGlob matches a single path segment with "*" and "?" wildcards that do
// not cross "/". Linear two-pointer greedy match with a single backtrack memo:
// on a mismatch it rewinds to just after the last "*" and lets it consume one
// more byte. Worst case O(len(p)·len(t)); no exponential backtracking.
func segmentGlob(p, t string) bool {
	var (
		pi, ti    int
		star      = -1 // index in p of the most recent '*', or -1
		starMatch int  // t index that '*' is currently pinned at
	)
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star = pi
			starMatch = ti
			pi++
		case star != -1:
			pi = star + 1
			starMatch++
			ti = starMatch
		default:
			return false
		}
	}
	// Trailing "*" wildcards match the empty rest of the segment.
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
