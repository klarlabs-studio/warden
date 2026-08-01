package domain

import (
	"strconv"
	"strings"
)

// SpanRecordingSince is the first warden release that recorded a push span in
// the provenance note (CoversFrom, added for #86 and shipped in v0.19.0).
//
// It is the dividing line for reading a gap. warden validates ONE tree per run,
// so the intermediate commits of a multi-commit push get no note of their own
// and never did — the span is what vouches for them. Before this version there
// was no span to write, so a gap next to such a note is genuinely ambiguous: an
// intermediate commit of a gated push and a real bypass look identical, and no
// amount of later analysis can separate them because the distinguishing
// information was never recorded.
//
// After it, the absence of a covering span IS evidence, and a gap can honestly
// be called a bypass.
const SpanRecordingSince = "0.19.0"

// PredatesSpanRecording reports whether a warden version string is older than
// the release that began recording push spans.
//
// An unparseable or empty version is treated as PREDATING it. Notes written by
// an unknown warden cannot be assumed to carry a span, and the fail-safe
// direction here is to under-claim: reporting a commit as unattributable when it
// might have been a bypass is a smaller error than reporting it as a bypass when
// it might have been a perfectly gated push.
func PredatesSpanRecording(version string) bool {
	return compareVersions(strings.TrimPrefix(strings.TrimSpace(version), "v"), SpanRecordingSince) < 0
}

// compareVersions compares dotted numeric versions, returning -1, 0 or 1. A
// segment that does not parse sorts as 0, and a version with no parseable
// leading segment sorts below everything — see PredatesSpanRecording for why
// that direction is the safe one.
func compareVersions(a, b string) int {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// splitVersion turns "0.19.0" into [0 19 0], stopping at the first pre-release
// or build suffix so "0.19.0-rc1" compares as 0.19.0 rather than as nothing.
func splitVersion(v string) []int {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return []int{-1} // sorts below any real version
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return append(out, -1)
		}
		out = append(out, n)
	}
	return out
}
