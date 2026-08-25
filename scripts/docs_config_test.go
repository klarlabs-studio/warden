package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every `.warden.yaml` key must appear in the README.
//
// WHY THIS EXISTS. Three documentation gaps in three days, each the same shape:
// a configuration surface shipped and was documented nowhere. `status.enabled`,
// `warden evidence` and `--approvals` went three releases undocumented;
// `forge.accept_authored` — a setting that changes what the GATE ACCEPTS —
// shipped in 0.30.0 with no mention anywhere; and the branch-rule reporting was
// undocumented within an hour of merging.
//
// None of those was carelessness, which is why remembering keeps not working.
// Docs written inside a feature's own pull request are correct when that pull
// request merges and stale the moment the next one does. Every individual diff
// is right; only the assembled repository is wrong, and nobody reads the
// assembled repository. The changelog had the identical problem four times and
// a test ended it. This is that test for configuration.
//
// WHAT IT DOES NOT CHECK. That the documentation is any good — only that the
// key is mentioned somewhere as a key. A field named in passing satisfies this
// and could still be useless prose, so this is a floor, not a standard.
const (
	readmePath = "../README.md"
	domainDir  = "../internal/domain"
)

// yamlTag finds the struct tags that define the config surface.
var yamlTag = regexp.MustCompile(`yaml:"([a-z0-9_]+)"`)

// documentedAs matches a key written as YAML — `field:` — not merely the word.
//
// The boundary class matters and was got wrong first: a bare substring search
// passes for every short key ("add", "base", "mode", "path") against any 60KB
// document, which is a check that always passes and therefore checks nothing.
// A shell version of this used `(^|[^a-z_])` and silently failed to match
// `add:` inside `{ insert_after: lint, add: [x] }`, which would have red-lined
// CI over a field that IS documented. Go's regexp handles the alternation
// correctly; the shell's did not.
func documentedAs(field string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)(^|[^a-zA-Z0-9_-])` + regexp.QuoteMeta(field) + `:`)
}

// exempt lists keys that must NOT be documented as live configuration, with the
// reason. An exemption is a claim like any other and carries its justification.
var exempt = map[string]string{
	"materialize_deps": "deprecated: still parsed for backward compatibility but no " +
		"longer changes behavior (see internal/domain/config.go). Documenting a no-op " +
		"as live configuration would be worse than omitting it.",
}

func configKeys(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(domainDir)
	if err != nil {
		t.Fatalf("read %s: %v", domainDir, err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(domainDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range yamlTag.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		t.Fatal("no yaml tags found; the config moved and this check is now blind")
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestREADME_DocumentsEveryConfigKey(t *testing.T) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	for _, key := range configKeys(t) {
		if why, ok := exempt[key]; ok {
			// An exemption that has become wrong is worth catching too: if the
			// key IS documented, the exemption is stale and should go.
			if documentedAs(key).Match(readme) {
				t.Errorf("%q is exempt but IS documented — drop the exemption (reason was: %s)", key, why)
			}
			continue
		}
		if !documentedAs(key).Match(readme) {
			t.Errorf("`.warden.yaml` key %q appears in no README section.\n"+
				"  Add it where a reader configuring warden would look, or exempt it with a reason.\n"+
				"  This recurs because docs written in a feature's own PR go stale when the next one merges.", key)
		}
	}
}

// The check must be able to fail. A pattern that matched anything would report
// full coverage forever, which is the failure mode it exists to prevent.
func TestREADME_ConfigCheckCanActuallyFail(t *testing.T) {
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if documentedAs("a_key_that_does_not_exist_anywhere").Match(readme) {
		t.Error("the matcher accepts a key that is not in the README — it proves nothing")
	}
	// And it must accept a key written inline rather than at line start, which
	// an earlier shell version did not.
	if !documentedAs("add").Match(readme) {
		t.Error("the matcher misses `add:` written inline; it would red-line documented fields")
	}
}
