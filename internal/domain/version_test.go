package domain

import "testing"

func TestPredatesSpanRecording(t *testing.T) {
	cases := map[string]bool{
		"0.17.0":      true,  // mcp-go's notes — two releases before spans
		"0.18.9":      true,  // the last release without them
		"0.19.0":      false, // the release that introduced them
		"0.19.0-rc1":  false, // a pre-release of it still has the field
		"0.21.1":      false,
		"1.0.0":       false,
		"v0.17.0":     true, // a leading v is tolerated
		"  0.17.0   ": true, // as is surrounding whitespace
	}
	for v, want := range cases {
		if got := PredatesSpanRecording(v); got != want {
			t.Errorf("PredatesSpanRecording(%q) = %v, want %v", v, got, want)
		}
	}
}

// An unknown version must be treated as predating spans. Reporting a commit as
// unattributable when it might have been a bypass is a smaller error than
// reporting it as a bypass when it might have been a perfectly gated push — the
// second accuses someone of something the data does not show.
func TestPredatesSpanRecording_UnknownVersionsFailSafe(t *testing.T) {
	for _, v := range []string{"", "   ", "unknown", "not-a-version", "v", "..."} {
		if !PredatesSpanRecording(v) {
			t.Errorf("PredatesSpanRecording(%q) = false; an unreadable version must be treated as pre-span", v)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.19.0", "0.19.0", 0},
		{"0.18.0", "0.19.0", -1},
		{"0.20.0", "0.19.0", 1},
		{"0.19", "0.19.0", 0},   // missing segments are zero
		{"1.0.0", "0.99.99", 1}, // major dominates
		{"0.19.1", "0.19.0", 1}, // patch is compared
		{"0.9.0", "0.19.0", -1}, // numeric, not lexical: 9 < 19
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The four states a commit without its own note can be in, now that the absence
// of a span is only evidence when a span could have been written.
func TestCommitStatus_UnattributableIsNotBypassed(t *testing.T) {
	preSpan := CommitStatus{SHA: "a", PreSpanProvenance: true}
	if !preSpan.Unattributable() {
		t.Error("a gap beside pre-span provenance must be unattributable")
	}
	if preSpan.Bypassed() {
		t.Error("…and must NOT be counted as a bypass: the data cannot support that claim")
	}

	modern := CommitStatus{SHA: "b"}
	if modern.Unattributable() {
		t.Error("a gap beside modern provenance is not unattributable")
	}
	if !modern.Bypassed() {
		t.Error("…it is a bypass: a span would have been recorded had it been gated")
	}
}

// A commit that IS covered or reattestable is neither, whatever the provenance
// era — those are positive findings and outrank the ambiguity.
func TestCommitStatus_PositiveFindingsOutrankAmbiguity(t *testing.T) {
	covered := CommitStatus{SHA: "a", CoveredBy: "tip", PreSpanProvenance: true}
	if covered.Unattributable() || covered.Bypassed() {
		t.Errorf("a span-covered commit is verified, not ambiguous: %+v", covered)
	}
	reattestable := CommitStatus{SHA: "b", ReattestableFrom: "pre", PreSpanProvenance: true}
	if reattestable.Unattributable() || reattestable.Bypassed() {
		t.Errorf("a reattestable commit is recoverable, not ambiguous: %+v", reattestable)
	}
}
