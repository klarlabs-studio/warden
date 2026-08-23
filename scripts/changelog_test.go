package scripts_test

import (
	"os"
	"strings"
	"testing"
)

// The changelog is a release artifact. It is what a user reads to decide
// whether to upgrade and what an auditor reads to date a control change, so it
// gets the same regression pressure as the binary.
//
// WHY THIS EXISTS. Duplicate `### Fixed` / `### Changed` headings inside one
// release have appeared four times: 0.12.0 and 0.19.0 carried them for months,
// 0.29.2 SHIPPED with a byte-identical `### Fixed` section repeated, and the
// 0.30.0 unreleased section had grown two before it was promoted.
//
// It is not carelessness. Several pull requests each append their own section
// to `## [Unreleased]`, every one of those diffs is correct in isolation, and
// nothing looks at the result. A reviewer sees "+### Fixed" and a bullet, which
// is exactly what a new fix should look like. Only the assembled file is wrong,
// and nobody reads the assembled file. So the check has to live here.
const changelogPath = "../CHANGELOG.md"

// releaseSections splits the changelog into (heading, subsection headings) per
// release, in file order.
func releaseSections(t *testing.T) []struct {
	Release string
	Subs    []string
	Lines   []int
} {
	t.Helper()
	raw, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	var out []struct {
		Release string
		Subs    []string
		Lines   []int
	}
	for i, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			out = append(out, struct {
				Release string
				Subs    []string
				Lines   []int
			}{Release: strings.TrimSpace(line)})
		case strings.HasPrefix(line, "### ") && len(out) > 0:
			last := &out[len(out)-1]
			last.Subs = append(last.Subs, strings.TrimSpace(line))
			last.Lines = append(last.Lines, i+1)
		}
	}
	if len(out) == 0 {
		t.Fatal("no release headings found; the changelog format changed and this check is now blind")
	}
	return out
}

// One section per change type per release. Keep a Changelog says so, and the
// reason it matters here is that a reader scanning for "what was fixed" stops
// at the first `### Fixed` and never learns there is a second one further down.
func TestChangelog_NoDuplicateSectionsWithinARelease(t *testing.T) {
	for _, rel := range releaseSections(t) {
		seen := map[string]int{}
		for i, sub := range rel.Subs {
			if prev, dup := seen[sub]; dup {
				t.Errorf("%s has %q twice, at lines %d and %d — merge them "+
					"(this recurs when several PRs each append a section to one release)",
					rel.Release, sub, prev, rel.Lines[i])
				continue
			}
			seen[sub] = rel.Lines[i]
		}
	}
}

// Keep a Changelog defines the vocabulary. An invented heading — "### Notes",
// "### Misc" — reads fine in a diff and quietly drops the change out of every
// tool and habit that looks for the standard set.
func TestChangelog_UsesTheStandardSectionVocabulary(t *testing.T) {
	allowed := map[string]bool{
		"### Added": true, "### Changed": true, "### Deprecated": true,
		"### Removed": true, "### Fixed": true, "### Security": true,
		// A deliberate local extension, in use since 0.20.1. warden ships
		// documentation as a product surface — a README claim that overstates
		// what the gate proves is the same defect class as code that does — so
		// those changes get their own section rather than being filed under
		// Changed. Listed explicitly so the set stays a decision, not a drift.
		"### Documentation": true,
	}
	for _, rel := range releaseSections(t) {
		for i, sub := range rel.Subs {
			if !allowed[sub] {
				t.Errorf("%s line %d: %q is not a Keep a Changelog section "+
					"(Added, Changed, Deprecated, Removed, Fixed, Security)",
					rel.Release, rel.Lines[i], sub)
			}
		}
	}
}

// A released section that says nothing is worse than an absent one: it asserts
// that a version shipped and describes none of it.
//
// CONTENT, not structure. The first draft of this required `###` subsections
// and failed 0.6.0, which describes the initial release in a plain paragraph —
// a correct entry that happens not to need the Added/Fixed split. Demanding the
// heading would have been this check asserting more than it can justify, in a
// file whose whole subject is claims outrunning their evidence.
func TestChangelog_EveryReleasedVersionDescribesSomething(t *testing.T) {
	raw, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read changelog: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	var cur string
	body := map[string]int{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			cur = trimmed
			body[cur] = 0
		case cur != "" && trimmed != "" && !strings.HasPrefix(trimmed, "["):
			// A link-definition line ("[0.6.0]: https://…") is reference
			// plumbing at the foot of the file, not a description.
			body[cur]++
		}
	}
	for rel, n := range body {
		if strings.Contains(rel, "[Unreleased]") {
			continue // empty is its correct state right after a release
		}
		if n == 0 {
			t.Errorf("%s describes nothing — a version that shipped with no account of it "+
				"cannot be read by anyone deciding whether to upgrade", rel)
		}
	}
}
