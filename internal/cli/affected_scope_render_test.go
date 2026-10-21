package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// affectedReport is a branch whose tip was gated by an affected-scope run that
// also published one intermediate commit, above a full-scope anchor.
func affectedReport() domain.AuditReport {
	day := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	return domain.AuditReport{Branch: "main", Adoption: "adopt000000000", Commits: []domain.CommitStatus{
		{SHA: "tip000000000001", Date: day, Subject: "web change", HasNote: true, ChainIntact: true, RunID: "run_tip",
			Steps: []domain.StepName{"test"}, Affected: true, Scope: "affected 2/7"},
		{SHA: "mid000000000002", Date: day, Subject: "ui change", CoveredBy: "tip000000000001", Affected: true, Scope: "affected 2/7"},
		{SHA: "anc000000000003", Date: day, Subject: "fixture", HasNote: true, ChainIntact: true, RunID: "run_anc", Steps: []domain.StepName{"test"}},
	}}
}

// doctor and audit name the partial check, never with the verified glyph,
// and count it apart from verified and from the gaps doctor exits on.
func TestAffectedScope_DoctorAndAudit(t *testing.T) {
	r := affectedReport()
	var doc bytes.Buffer
	printDoctor(&doc, r)
	out := doc.String()
	for _, want := range []string{
		"~ tip000000000",
		"chain-intact, affected 2/7: partial check, not verified",
		"~ mid000000000",
		"covered by the gated push tip000000000, affected 2/7: partial check",
		"1 verified, 0 unverified since adoption; 2 gated by an affected-scope run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✓ tip") || strings.Contains(out, "✓ mid") {
		t.Errorf("an affected commit must not read as verified:\n%s", out)
	}
	if r.Counts().Unverified != 0 {
		t.Fatal("affected commits are no gap: doctor must not fail on them")
	}

	var text bytes.Buffer
	printAuditText(&text, r)
	for _, want := range []string{"~ tip000000000", "~ mid000000000", "2 gated by an affected-scope run"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("audit text missing %q:\n%s", want, text.String())
		}
	}
	var md bytes.Buffer
	printAuditMarkdown(&md, r)
	if !strings.Contains(md.String(), "gated, affected 2/7 (partial check, not verified)") {
		t.Errorf("audit markdown:\n%s", md.String())
	}
	var js, errb bytes.Buffer
	if code := printAuditJSON(&js, &errb, r); code != 0 {
		t.Fatal(errb.String())
	}
	var exp auditExport
	if err := json.Unmarshal(js.Bytes(), &exp); err != nil {
		t.Fatal(err)
	}
	if exp.Summary.Affected != 2 || exp.Summary.Verified != 1 || exp.Commits[0].Validated || exp.Commits[0].Scope != "affected 2/7" || !exp.Commits[2].Validated {
		t.Fatalf("audit json %+v", exp)
	}
}

// The evidence package puts affected commits on their own row: gated, not
// verified, and not exceptions either.
func TestAffectedScope_Evidence(t *testing.T) {
	ev := domain.NewEvidence(affectedReport(), domain.EvidenceOptions{Tool: "warden test", Repository: "/repo"})
	var md bytes.Buffer
	printEvidenceMarkdown(&md, ev)
	for _, want := range []string{"| Gated and verified | 1 |", "| Gated, affected scope | 2 |", "None. Every change in the period was gated"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("evidence markdown missing %q:\n%s", want, md.String())
		}
	}
	var js, errb bytes.Buffer
	if code := printEvidenceJSON(&js, &errb, ev); code != 0 {
		t.Fatal(errb.String())
	}
	for _, want := range []string{`"gated_affected_scope": 2`, `"gated_and_verified": 1`, `"state": "gated_affected_scope"`, `"scope": "affected 2/7"`} {
		if !strings.Contains(js.String(), want) {
			t.Errorf("evidence json missing %s:\n%s", want, js.String())
		}
	}
	var oscal bytes.Buffer
	if code := printEvidenceOSCAL(&oscal, &errb, ev); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(oscal.String(), "2 gated by an affected-scope (partial) check") || !strings.Contains(oscal.String(), "this is a partial check") {
		t.Errorf("oscal:\n%s", oscal.String())
	}
}

// The fleet rollup keeps every commit in exactly one bucket.
func TestAffectedScope_FleetBuckets(t *testing.T) {
	r := affectedReport()
	t2 := r.Counts()
	if t2.Affected != 2 || t2.Verified != 1 || t2.Covered != 0 || t2.Total() != len(r.Commits) {
		t.Fatalf("counts %+v", t2)
	}
	var b bytes.Buffer
	printFleet(&b, fleetReport{Adopted: 1, Commits: 3, Verified: 1, Affected: 2, Repos: []fleetRepo{{Path: "/x/mono", Adopted: true, Commits: 3, Verified: 1, Affected: 2}}})
	if !strings.Contains(b.String(), "2 gated by an affected-scope (partial) check") {
		t.Errorf("fleet:\n%s", b.String())
	}
}
