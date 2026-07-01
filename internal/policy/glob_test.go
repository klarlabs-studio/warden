package policy

import "testing"

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
