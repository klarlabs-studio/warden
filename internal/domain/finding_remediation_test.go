package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// Actionable is the question a reader actually asks of a finding: can I DO
// something with this, or does it only describe a problem?
func TestFinding_Actionable(t *testing.T) {
	cases := map[string]struct {
		finding Finding
		want    bool
	}{
		"no fix at all":     {Finding{Message: "line too long"}, false},
		"command":           {Finding{Fix: &Fix{Command: "gofmt -w ."}}, true},
		"patch":             {Finding{Fix: &Fix{Patch: "--- a\n+++ b\n"}}, true},
		"both":              {Finding{Fix: &Fix{Command: "x", Patch: "y"}}, true},
		"empty fix promise": {Finding{Fix: &Fix{}}, false},
	}
	for name, tc := range cases {
		if got := tc.finding.Actionable(); got != tc.want {
			t.Errorf("%s: Actionable() = %v, want %v", name, got, tc.want)
		}
	}
}

// An empty Fix must not read as actionable. A finding that promises a remedy and
// then hands over an empty string is worse than one that promises nothing: the
// reader acts on the promise and gets nowhere.
func TestFinding_EmptyFixIsNotAPromise(t *testing.T) {
	f := Finding{Message: "something", Fix: &Fix{}}
	if f.Actionable() {
		t.Error("an empty Fix must not report as actionable")
	}
}

// The remediation fields are additive: a finding that sets none of them must
// serialize exactly as it did before they existed, or every existing consumer of
// the wire format sees new keys it did not ask for.
func TestFinding_RemediationFieldsAreOmittedWhenUnset(t *testing.T) {
	data, err := json.Marshal(Finding{Severity: SeverityHigh, Message: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, absent := range []string{"rule", "why", "fix"} {
		if strings.Contains(got, absent) {
			t.Errorf("unset %q must be omitted from the wire form: %s", absent, got)
		}
	}
	if !strings.Contains(got, `"severity"`) || !strings.Contains(got, `"message"`) {
		t.Errorf("the original fields must still be present: %s", got)
	}
}

// When set, they must round-trip — this is the contract a subprocess step and
// the agent surfaces both rely on.
func TestFinding_RemediationRoundTrips(t *testing.T) {
	want := Finding{
		Severity: SeverityMedium,
		Message:  "unused variable",
		File:     "internal/foo.go",
		Line:     42,
		Rule:     "SA4006",
		Why:      "the value is never read, so the assignment is dead code",
		Fix:      &Fix{Command: "gofmt -w internal/foo.go"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Finding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Rule != want.Rule || got.Why != want.Why {
		t.Errorf("rule/why did not round-trip: %+v", got)
	}
	if got.Fix == nil || got.Fix.Command != want.Fix.Command {
		t.Errorf("fix did not round-trip: %+v", got.Fix)
	}
	if !got.Actionable() {
		t.Error("a finding with a fix command must be actionable after a round-trip")
	}
}
