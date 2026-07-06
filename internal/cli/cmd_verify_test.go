package cli

import "testing"

// TestParseRange pins the --range parser: it accepts a two-dot BASE..HEAD with
// both endpoints present, and rejects git's three-dot symmetric-difference form
// (a provenance gate must walk a definite ancestry range) and any spec with a
// missing endpoint or no separator.
func TestParseRange(t *testing.T) {
	cases := []struct {
		spec             string
		wantBase, wantHd string
		wantOK           bool
	}{
		{"origin/main..HEAD", "origin/main", "HEAD", true},
		{"abc123..def456", "abc123", "def456", true},
		{"main..feature/x", "main", "feature/x", true},
		{"origin/main...HEAD", "", "", false}, // three-dot rejected
		{"HEAD", "", "", false},               // no separator
		{"..HEAD", "", "", false},             // missing base
		{"main..", "", "", false},             // missing head
		{"", "", "", false},                   // empty
	}
	for _, tc := range cases {
		base, head, ok := parseRange(tc.spec)
		if ok != tc.wantOK || base != tc.wantBase || head != tc.wantHd {
			t.Errorf("parseRange(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.spec, base, head, ok, tc.wantBase, tc.wantHd, tc.wantOK)
		}
	}
}
