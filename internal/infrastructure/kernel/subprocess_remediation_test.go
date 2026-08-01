package kernel

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/stepsdk"
)

// A subprocess step's remediation must survive the crossing into the domain.
// Dropping it here would make the wire protocol advertise fields that silently
// vanish — worse than not having them, because a step author would fill them in
// and never learn they went nowhere.
func TestFromWireOutput_CarriesRemediation(t *testing.T) {
	out := stepsdk.Output{
		Status: "fail",
		Findings: []stepsdk.Finding{{
			Severity: "high",
			Message:  "unused import",
			File:     "main.go",
			Line:     7,
			Rule:     "ST1003",
			Why:      "an unused import fails the build on some toolchains",
			Fix:      &stepsdk.Fix{Command: "goimports -w main.go"},
		}},
	}
	res := fromWireOutput("lint", out)

	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.Rule != "ST1003" || f.Why == "" {
		t.Errorf("rule/why lost in translation: %+v", f)
	}
	if f.Fix == nil || f.Fix.Command != "goimports -w main.go" {
		t.Errorf("fix lost in translation: %+v", f.Fix)
	}
	if !f.Actionable() {
		t.Error("the finding should be actionable")
	}
}

// A step reporting only severity and message — every step written before these
// fields existed — must produce exactly what it always did.
func TestFromWireOutput_MinimalFindingIsUnchanged(t *testing.T) {
	res := fromWireOutput("lint", stepsdk.Output{
		Status:   "fail",
		Findings: []stepsdk.Finding{{Severity: "medium", Message: "nope"}},
	})
	f := res.Findings[0]
	if f.Rule != "" || f.Why != "" || f.Fix != nil {
		t.Errorf("nothing should be invented for a minimal finding: %+v", f)
	}
	if f.Actionable() {
		t.Error("a finding with no fix must not be actionable")
	}
}

// An empty Fix object on the wire must not become an actionable finding. A step
// that serializes `"fix": {}` — easy to do by accident with a zero-valued struct
// — would otherwise promise a remedy it does not have.
func TestFromWireOutput_EmptyFixIsDropped(t *testing.T) {
	res := fromWireOutput("lint", stepsdk.Output{
		Status:   "fail",
		Findings: []stepsdk.Finding{{Severity: "low", Message: "x", Fix: &stepsdk.Fix{}}},
	})
	if f := res.Findings[0]; f.Fix != nil {
		t.Errorf("an empty wire Fix must not survive as a domain Fix: %+v", f.Fix)
	}
}

// prior_findings must be a faithful view of the run so far. A step deciding
// whether to re-report an issue needs to see that an earlier step already
// offered a fix for it.
func TestToWireFindings_CarriesRemediationToTheNextStep(t *testing.T) {
	wire := toWireFindings([]domain.Finding{{
		Severity: domain.SeverityHigh,
		Message:  "secret",
		Rule:     "credentials/github-token",
		Why:      "already compromised once pushed",
		Fix:      &domain.Fix{Patch: "--- a\n+++ b\n"},
	}})
	if len(wire) != 1 {
		t.Fatalf("wire findings = %d, want 1", len(wire))
	}
	if wire[0].Rule == "" || wire[0].Why == "" {
		t.Errorf("rule/why must reach the next step: %+v", wire[0])
	}
	if wire[0].Fix == nil || wire[0].Fix.Patch == "" {
		t.Errorf("fix must reach the next step: %+v", wire[0].Fix)
	}
}

func TestToWireFindings_EmptyStaysNil(t *testing.T) {
	if got := toWireFindings(nil); got != nil {
		t.Errorf("no findings should stay nil, got %v", got)
	}
}
