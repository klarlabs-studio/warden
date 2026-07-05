package policy

import (
	"strings"
	"testing"
	"time"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		s       string
		want    bool
	}{
		// Exact / single segment.
		{"main", "main", true},
		{"main", "feature", false},
		{"release/*", "release/1.0", true},
		{"release/*", "release/1/0", false}, // * does not cross "/"
		// Double-star spans segments.
		{"security/**", "security/token.go", true},
		{"security/**", "security/aws/token.go", true},
		{"security/**", "security", true}, // ** matches zero segments
		{"auth/**", "authz/x.go", false},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"**/token.go", "security/aws/token.go", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		// Question mark.
		{"v?", "v1", true},
		{"v?", "v10", false},
		// Star within segment.
		{"docs/**", "docs/guide/intro.md", true},
		{"*.md", "readme.md", true},
		{"*.md", "docs/readme.md", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// TestGlobMatchReDoS guards against the exponential-backtracking DoS that the
// previous recursive matchers were vulnerable to. Patterns come from
// .warden.yaml (`paths:`/`cache:`), which may be pulled in untrusted via
// `extends:`, and are matched against diff paths / branch names. With the old
// backtrackers these inputs would hang the gate on `git push`; the linear
// two-pointer matcher must resolve them near-instantly.
//
// We assert both correctness (the right match/no-match) and a generous,
// CI-safe wall-clock bound. The old code took exponential time on these; the
// linear matcher finishes in microseconds, so a 2s ceiling is comfortably
// generous while still failing loudly on any regression to backtracking.
func TestGlobMatchReDoS(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		s       string
		want    bool
	}{
		{
			// Classic in-segment ReDoS: many "*a" groups against a run of
			// 'a's with no trailing 'b' — forces the old backtracker to try
			// every split. No 'b' in the text, so it must NOT match.
			name:    "star-a-no-trailing-b",
			pattern: strings.Repeat("*a", 32) + "*b",
			s:       strings.Repeat("a", 64),
			want:    false,
		},
		{
			// Same in-segment pattern, but the text ends in 'b' so it DOES
			// match — exercises the successful path at speed.
			name:    "star-a-trailing-b",
			pattern: strings.Repeat("*a", 32) + "*b",
			s:       strings.Repeat("a", 64) + "b",
			want:    true,
		},
		{
			// Cross-segment ReDoS: many "**/b" groups against a deep path of
			// 'a' segments with no 'b' — the old segment-level backtracker
			// blows up. No 'b' segment, so it must NOT match.
			name:    "deep-doublestar-b-no-match",
			pattern: "a/" + strings.Repeat("**/b/", 24) + "**",
			s:       "a/" + strings.Repeat("a/", 60) + "a",
			want:    false,
		},
		{
			// Deep "**"-only pattern against a deep path: matches, fast.
			name:    "deep-doublestar-match",
			pattern: strings.Repeat("**/", 24) + "x",
			s:       strings.Repeat("a/", 60) + "x",
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan bool, 1)
			go func() { done <- globMatch(c.pattern, c.s) }()
			select {
			case got := <-done:
				if got != c.want {
					t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("globMatch(%q, %q) did not return within 2s — "+
					"likely exponential backtracking (ReDoS regression)", c.pattern, c.s)
			}
		})
	}
}

// TestGlobMatchCaps verifies the defense-in-depth size caps reject
// pathologically large input without affecting realistic globs.
func TestGlobMatchCaps(t *testing.T) {
	// Over the byte cap -> no match, returns immediately.
	if globMatch(strings.Repeat("*", maxGlobLen+1), strings.Repeat("a", 10)) {
		t.Error("oversized pattern should not match")
	}
	if globMatch("*", strings.Repeat("a", maxGlobLen+1)) {
		t.Error("oversized subject should not match")
	}
	// Over the segment cap -> no match.
	if globMatch(strings.Repeat("*/", maxGlobSegments+1), strings.Repeat("a/", 3)+"a") {
		t.Error("too many pattern segments should not match")
	}
}
