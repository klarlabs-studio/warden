package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// writeReport drops a findings.json shaped like nox's into a fresh dir.
func writeReport(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ReportFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const twoFindings = `{
  "meta": {"tool_version": "1.15.0"},
  "findings": [
    {"ID":"SEC-001-a","RuleID":"SEC-001","Severity":"high","Fingerprint":"aaa",
     "Message":"AWS Access Key ID detected","Location":{"FilePath":"main.go","StartLine":2}},
    {"ID":"CRYPTO-001-b","RuleID":"CRYPTO-001","Severity":"medium","Fingerprint":"bbb",
     "Message":"weak hash","Location":{"FilePath":"weak.go","StartLine":7}}
  ]
}`

func TestReadReport(t *testing.T) {
	rep, err := ReadReport(writeReport(t, twoFindings))
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if rep.ToolVersion != "1.15.0" {
		t.Errorf("ToolVersion = %q, want 1.15.0", rep.ToolVersion)
	}
	if len(rep.Findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(rep.Findings))
	}
	f := rep.Findings[0]
	if f.Fingerprint != "aaa" || f.RuleID != "SEC-001" || f.Severity != "high" || f.File != "main.go" || f.Line != 2 {
		t.Errorf("first finding = %+v, want the SEC-001 hit at main.go:2", f)
	}
}

func TestReadReport_MissingOrMalformedIsAnError(t *testing.T) {
	// Silently reading "no report" as "no findings" would turn a scanner crash
	// into a green gate, so both cases must surface as errors and let the caller
	// fall back to exit-code gating.
	if _, err := ReadReport(t.TempDir()); err == nil {
		t.Error("a missing report was read as success")
	}
	if _, err := ReadReport(writeReport(t, "{not json")); err == nil {
		t.Error("a malformed report was read as success")
	}
}

func TestReport_Gating(t *testing.T) {
	rep, err := ReadReport(writeReport(t, twoFindings))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("threshold filters the report, which the scanner does not", func(t *testing.T) {
		// findings.json carries every hit regardless of -severity-threshold, so a
		// repo that gates on high must not be failed by a medium.
		got := rep.Gating("high")
		if len(got) != 1 || got[0].RuleID != "SEC-001" {
			t.Errorf("Gating(high) = %+v, want only the high SEC-001", got)
		}
	})

	t.Run("no threshold gates on everything", func(t *testing.T) {
		if got := rep.Gating(""); len(got) != 2 {
			t.Errorf("Gating(\"\") returned %d findings, want 2", len(got))
		}
	})

	t.Run("critical outranks high", func(t *testing.T) {
		r := Report{Findings: []Finding{{RuleID: "SEC-030", Severity: "critical", Fingerprint: "c"}}}
		if got := r.Gating("high"); len(got) != 1 {
			t.Error("a critical finding was dropped by a high threshold")
		}
	})
}

func TestReport_GatingSkipsSuppressedButNotUnknownStatuses(t *testing.T) {
	r := Report{Findings: []Finding{
		{RuleID: "A", Severity: "high", Status: "baselined", Fingerprint: "1"},
		{RuleID: "B", Severity: "high", Status: "suppressed", Fingerprint: "2"},
		{RuleID: "C", Severity: "high", Status: "open", Fingerprint: "3"},
		{RuleID: "D", Severity: "high", Status: "", Fingerprint: "4"},
		// A disposition warden does not recognize must still gate: a new scanner
		// status must not be able to quietly switch the gate off.
		{RuleID: "E", Severity: "high", Status: "quarantined", Fingerprint: "5"},
	}}
	got := r.Gating("high")
	if len(got) != 3 {
		t.Fatalf("Gating returned %d findings, want 3 (C, D, E); got %+v", len(got), got)
	}
	for i, want := range []string{"C", "D", "E"} {
		if got[i].RuleID != want {
			t.Errorf("gating[%d] = %s, want %s", i, got[i].RuleID, want)
		}
	}
}

func TestSplitIntroduced(t *testing.T) {
	gating := []Finding{
		{RuleID: "OLD", Fingerprint: "aaa"},
		{RuleID: "NEW", Fingerprint: "zzz"},
		{RuleID: "NOFP"},
	}
	introduced, preexisting := SplitIntroduced(gating, map[string]bool{"aaa": true})

	if len(preexisting) != 1 || preexisting[0].RuleID != "OLD" {
		t.Errorf("pre-existing = %+v, want the finding already present at the base", preexisting)
	}
	// A finding with no fingerprint cannot be matched, so it fails closed.
	if len(introduced) != 2 || introduced[0].RuleID != "NEW" || introduced[1].RuleID != "NOFP" {
		t.Errorf("introduced = %+v, want NEW and the unfingerprinted finding", introduced)
	}
}

func TestReport_FingerprintsIncludesSuppressed(t *testing.T) {
	// The base set answers "did the scanner already see this here", so a finding
	// that was baselined at the base commit must count as pre-existing —
	// otherwise removing a baseline entry would block an unrelated push.
	r := Report{Findings: []Finding{
		{Fingerprint: "aaa", Status: "baselined"},
		{Fingerprint: "bbb"},
		{Fingerprint: ""},
	}}
	got := r.Fingerprints()
	if len(got) != 2 || !got["aaa"] || !got["bbb"] {
		t.Errorf("Fingerprints() = %v, want {aaa, bbb}", got)
	}
}

func TestReadBaseline(t *testing.T) {
	root := t.TempDir()

	t.Run("a repo with no baseline is not an error", func(t *testing.T) {
		_, found, err := ReadBaseline(root)
		if err != nil || found {
			t.Errorf("ReadBaseline on a bare repo = (found %v, err %v), want (false, nil)", found, err)
		}
	})

	if err := os.MkdirAll(filepath.Join(root, ".nox"), 0o750); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":"1.0.0","entries":[
		{"fingerprint":"aaa","rule_id":"SEC-001","severity":"high"},
		{"fingerprint":"bbb","rule_id":"SEC-002","severity":"medium"}]}`
	if err := os.WriteFile(filepath.Join(root, BaselinePath), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	b, found, err := ReadBaseline(root)
	if err != nil || !found {
		t.Fatalf("ReadBaseline = (found %v, err %v), want (true, nil)", found, err)
	}
	if len(b.Fingerprints) != 2 {
		t.Errorf("got %d baseline fingerprints, want 2", len(b.Fingerprints))
	}
}

func TestBaselineDrift(t *testing.T) {
	baseline := Baseline{Fingerprints: []string{"aaa", "bbb", "ccc"}}

	t.Run("a total miss against a live report is drift", func(t *testing.T) {
		// This is the incident signature: the scanner renumbered its rules, so
		// every fingerprint changed at once and 729 triaged entries matched
		// nothing while the gate reported 240 phantom criticals.
		rep := Report{Findings: []Finding{{Fingerprint: "xxx"}, {Fingerprint: "yyy"}}}
		drifted, matched := BaselineDrift(baseline, rep)
		if !drifted || matched != 0 {
			t.Errorf("BaselineDrift = (%v, %d), want (true, 0)", drifted, matched)
		}
	})

	t.Run("a partial match is normal churn, not drift", func(t *testing.T) {
		rep := Report{Findings: []Finding{{Fingerprint: "aaa"}, {Fingerprint: "xxx"}}}
		if drifted, matched := BaselineDrift(baseline, rep); drifted || matched != 1 {
			t.Errorf("BaselineDrift = (%v, %d), want (false, 1)", drifted, matched)
		}
	})

	t.Run("a clean tree is a fixed repo, not drift", func(t *testing.T) {
		// Nothing to match against is what success looks like; calling it drift
		// would make every healthy repo shout.
		if drifted, _ := BaselineDrift(baseline, Report{}); drifted {
			t.Error("an empty report was reported as baseline drift")
		}
	})

	t.Run("no baseline cannot drift", func(t *testing.T) {
		rep := Report{Findings: []Finding{{Fingerprint: "xxx"}}}
		if drifted, _ := BaselineDrift(Baseline{}, rep); drifted {
			t.Error("a repo with no baseline was reported as drifted")
		}
	})
}

func TestFinding_String(t *testing.T) {
	f := Finding{RuleID: "SEC-030", Severity: "critical", File: "main.go", Line: 3, Message: "Stripe API Key detected"}
	// The scanner's own severity word is kept: warden's Finding scale tops out
	// at "high", and a report that silently downgrades a critical is worse than
	// no report.
	want := "SEC-030 (critical) main.go:3 Stripe API Key detected"
	if got := f.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
