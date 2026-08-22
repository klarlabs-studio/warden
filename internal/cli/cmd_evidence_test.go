package cli

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

func sampleEvidence(t *testing.T) domain.Evidence {
	t.Helper()
	day := func(d int) string {
		return time.Date(2026, 2, d, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	}
	report := domain.AuditReport{
		Branch:   "main",
		Adoption: "a1b2c3d4e5f6aaaabbbbcccc",
		Commits: []domain.CommitStatus{
			{SHA: "a1b2c3d4e5f60001", Date: day(3), Author: "dev", Subject: "gated change",
				HasNote: true, ChainIntact: true, RunID: "run_1", Steps: []domain.StepName{"test", "lint"}},
			{SHA: "a1b2c3d4e5f60002", Date: day(4), Author: "dev", Subject: "bypassed change"},
			{SHA: "a1b2c3d4e5f60003", Date: day(5), Author: "dev", Subject: "squashed",
				ReattestableFrom: "a1b2c3d4e5f60004"},
		},
	}
	return domain.NewEvidence(report, domain.EvidenceOptions{
		Tool: "warden test", Repository: "/repo",
		From: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
		Now:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})
}

// The Markdown is read by whoever signs the opinion. It has to carry the
// population, the exceptions with reasons, the limits, and the command that
// re-derives the verdict without trusting the document.
func TestEvidenceMarkdown_CarriesWhatAnAuditorNeeds(t *testing.T) {
	var out bytes.Buffer
	printEvidenceMarkdown(&out, sampleEvidence(t))
	md := out.String()

	for _, want := range []string{
		"Every change on the branch in the period, classified. Not a sample.",
		"bypassed change", // the exception is named
		"warden reattest", // and the remediable one says how
		"Not evidenced.",  // per-control limits
		"Not supported:",  // package-level limits
		"warden verify --range",
		"Evidence digest",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

// A zero exception count must read as a positive statement, not a blank
// section an auditor has to interpret.
func TestEvidenceMarkdown_SaysSoWhenThereAreNoExceptions(t *testing.T) {
	report := domain.AuditReport{Branch: "main", Adoption: "a", Commits: []domain.CommitStatus{
		{SHA: "1", Date: time.Now().UTC().Format(time.RFC3339), HasNote: true, ChainIntact: true},
	}}
	var out bytes.Buffer
	printEvidenceMarkdown(&out, domain.NewEvidence(report, domain.EvidenceOptions{}))

	if !strings.Contains(out.String(), "None. Every change in the period was gated") {
		t.Error("an empty exception list should state that explicitly")
	}
}

func TestEvidenceJSON_IsVersionedAndComplete(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceJSON(&out, &errOut, sampleEvidence(t)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	var doc evidenceJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc.Schema != evidenceSchema {
		t.Errorf("schema = %q, want %q", doc.Schema, evidenceSchema)
	}
	if doc.Summary.Total != 3 || len(doc.Population) != 3 {
		t.Errorf("population = %d, summary total = %d, want 3 and 3", len(doc.Population), doc.Summary.Total)
	}
	if len(doc.Exceptions) != 2 {
		t.Errorf("exceptions = %d, want 2", len(doc.Exceptions))
	}
	if doc.Digest == "" || doc.Verify == "" {
		t.Error("a consumer cannot re-verify without the digest and the command")
	}
	if len(doc.Assertions.Unsupported) == 0 {
		t.Error("the JSON dropped the limits; a platform would ingest an overclaim")
	}
	// A commit's state must be one of the four the report explains, or a
	// platform's dashboard invents a fifth meaning.
	for _, c := range doc.Population {
		switch c.State {
		case "verified", "covered", "exception", "outside_control":
		default:
			t.Errorf("commit %s has unknown state %q", c.SHA, c.State)
		}
	}
}

// Findings and observations must not overlap: a commit is evidence that the
// control worked, or evidence that it did not.
func TestEvidenceOSCAL_PartitionsObservationsAndFindings(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceOSCAL(&out, &errOut, sampleEvidence(t)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	var doc oscalDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc.AssessmentResults.Metadata.OSCALVersion != oscalVersion {
		t.Errorf("oscal-version = %q", doc.AssessmentResults.Metadata.OSCALVersion)
	}
	if len(doc.AssessmentResults.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(doc.AssessmentResults.Results))
	}
	r := doc.AssessmentResults.Results[0]
	if len(r.Observations) != 1 {
		t.Errorf("observations = %d, want the one gated commit", len(r.Observations))
	}
	if len(r.Findings) != 2 {
		t.Errorf("findings = %d, want the two unvouched commits", len(r.Findings))
	}
	if !strings.Contains(r.Remarks, "does NOT evidence") {
		t.Error("OSCAL remarks dropped the limits")
	}
	if len(r.ReviewedControls.ControlSelections) == 0 ||
		len(r.ReviewedControls.ControlSelections[0].IncludeControls) == 0 {
		t.Error("no controls reviewed")
	}
}

// Consecutive periods get diffed. Identifiers that churned on every run would
// make every commit look like it changed.
func TestEvidenceOSCAL_IdentifiersAreDerivedNotRandom(t *testing.T) {
	first, second := &bytes.Buffer{}, &bytes.Buffer{}
	e := sampleEvidence(t)
	_ = printEvidenceOSCAL(first, &bytes.Buffer{}, e)
	_ = printEvidenceOSCAL(second, &bytes.Buffer{}, e)

	if first.String() != second.String() {
		t.Error("two renderings of the same evidence differ")
	}
}

// SOC 2 has no OSCAL catalog. Emitting a bare "cc8.1" invites a consumer to
// resolve it against NIST 800-53, where it means nothing.
func TestEvidenceOSCAL_NamespacesControlIDs(t *testing.T) {
	got := oscalControlID2(domain.Control{Framework: "SOC 2", ID: "CC8.1"})
	if got != "soc-2-cc8.1" {
		t.Errorf("control-id = %q, want a namespaced id", got)
	}
}

func TestParsePeriodBound(t *testing.T) {
	if _, err := parsePeriodBound("nonsense", false); err == nil {
		t.Error("accepted a date that is not a date")
	}
	if got, err := parsePeriodBound("", false); err != nil || !got.IsZero() {
		t.Errorf("empty bound = %v, %v; want the zero time", got, err)
	}
	// The end bound covers its whole day: a --to that excluded most of the
	// last day would drop commits from a population reconciled by hand.
	end, err := parsePeriodBound("2026-03-31", true)
	if err != nil {
		t.Fatal(err)
	}
	lateThatDay := time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC)
	if end.Before(lateThatDay) {
		t.Errorf("--to 2026-03-31 = %s, excludes %s", end, lateThatDay)
	}
}

func TestEvidenceCmd_RejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"--format", "pdf"},
		{"--from", "March"},
		{"--to", "2026-13-45"},
		{"--from", "2026-03-01", "--to", "2026-01-01"},
		{"--frameworks", "hipaa"},
	} {
		var out, errOut bytes.Buffer
		if code := cmdEvidence(args, &out, &errOut); code != 2 {
			t.Errorf("cmdEvidence(%v) = %d, want 2 (usage)", args, code)
		}
		if errOut.Len() == 0 {
			t.Errorf("cmdEvidence(%v) refused silently", args)
		}
	}
}

// A stale version in an evidence header is a false statement in a document
// somebody signs, so an unstamped binary reports what it was built from.
func TestResolveVersion(t *testing.T) {
	stamped := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := resolveVersion("1.2.3", stamped); got != "1.2.3" {
		t.Errorf("ldflags version = %q, want it to win", got)
	}

	withModule := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.29.0"}}, true
	}
	if got := resolveVersion(defaultVersion, withModule); got != "v0.29.0" {
		t.Errorf("unstamped build = %q, want the module version", got)
	}

	withRevision := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "(devel)"},
			// Repetitive on purpose: a realistic-looking hex revision reads as a
			// high-entropy string to the secret scanner, and a fixture is not
			// worth a suppression.
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcabcabcabcabcabc"}},
		}, true
	}
	if got := resolveVersion(defaultVersion, withRevision); got != "dev+abcabcabcabc" {
		t.Errorf("local build = %q, want dev+<revision>", got)
	}

	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := resolveVersion(defaultVersion, noInfo); got != defaultVersion {
		t.Errorf("no build info = %q, want the literal", got)
	}
}

// approvalEvidence is the sample population with forge review records attached:
// one independently approved, one self-approved, one nobody approved.
func approvalEvidence(t *testing.T) domain.Evidence {
	t.Helper()
	e := sampleEvidence(t)
	return e.WithApprovals(map[string]domain.Approval{
		"a1b2c3d4e5f60001": {Found: true, PR: 10, Author: "alice", Approvers: []string{"bob"}},
		"a1b2c3d4e5f60002": {Found: true, PR: 11, Author: "alice", Approvers: []string{"alice"}},
		"a1b2c3d4e5f60003": {Found: true, PR: 12, Author: "alice"},
	})
}

// The four approval states must survive into the Markdown, and the shortfall
// has to be stated rather than left for the reader to subtract.
func TestEvidenceMarkdown_ReportsSeparationOfDuties(t *testing.T) {
	var out bytes.Buffer
	printEvidenceMarkdown(&out, approvalEvidence(t))
	md := out.String()

	for _, want := range []string{
		"Separation of duties",
		"Approved by someone other than the author",
		"Self-approved only",
		"did not carry an independent approval",
		"compensating-control narrative",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

// Not collecting approvals and collecting them but finding none are different
// facts; a report that never asked must not show a table of zeros.
func TestEvidenceMarkdown_SaysNothingAboutApprovalsWhenNotCollected(t *testing.T) {
	var out bytes.Buffer
	printEvidenceMarkdown(&out, sampleEvidence(t))
	if strings.Contains(out.String(), "Separation of duties") {
		t.Error("rendered an approval section for a report that never asked the forge")
	}
}

func TestEvidenceJSON_CarriesApprovalsPerCommitAndInSummary(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceJSON(&out, &errOut, approvalEvidence(t)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc evidenceJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Approvals == nil {
		t.Fatal("no separation_of_duties block")
	}
	if doc.Approvals.Independent != 1 || doc.Approvals.SelfApprovedOnly != 1 || doc.Approvals.Unapproved != 1 {
		t.Errorf("summary = %+v", *doc.Approvals)
	}
	var withPR int
	for _, c := range doc.Population {
		if c.PR != 0 {
			withPR++
			if c.PRAuthor == "" {
				t.Errorf("commit %s has a PR but no author", c.SHA)
			}
		}
	}
	if withPR != 3 {
		t.Errorf("population rows carrying a PR = %d, want 3", withPR)
	}
}

// A report that never asked must omit the block entirely rather than emit
// zeros a platform would render as "no approvals found".
func TestEvidenceJSON_OmitsApprovalsWhenNotCollected(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceJSON(&out, &errOut, sampleEvidence(t)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc evidenceJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Approvals != nil {
		t.Errorf("emitted an approval summary unasked: %+v", *doc.Approvals)
	}
}

// The OSCAL observation should distinguish an approved change from a
// self-approved one in words, since that is what a reader of the assessment
// sees.
func TestEvidenceOSCAL_DescribesApprovalState(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceOSCAL(&out, &errOut, approvalEvidence(t)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	body := out.String()
	if !strings.Contains(body, "approved by bob") {
		t.Error("an independent approval is not described")
	}
}

func TestSplitFrameworks(t *testing.T) {
	got := splitFrameworks(" SOC2 , iso27001 ,, ")
	if len(got) != 2 || got[0] != "soc2" || got[1] != "iso27001" {
		t.Errorf("splitFrameworks = %v", got)
	}
	if splitFrameworks("  ") != nil {
		t.Error("blank should mean no selection")
	}
}

func TestPeriodLabelCoversEveryBound(t *testing.T) {
	e := sampleEvidence(t)
	if got := periodLabel(e); !strings.Contains(got, "to") {
		t.Errorf("both bounds = %q", got)
	}
	open := e
	open.From, open.To = time.Time{}, time.Time{}
	if got := periodLabel(open); got != "all history since adoption" {
		t.Errorf("no bounds = %q", got)
	}
	fromOnly := e
	fromOnly.To = time.Time{}
	if got := periodLabel(fromOnly); !strings.HasPrefix(got, "from ") {
		t.Errorf("from only = %q", got)
	}
	toOnly := e
	toOnly.From = time.Time{}
	if got := periodLabel(toOnly); !strings.HasPrefix(got, "up to ") {
		t.Errorf("to only = %q", got)
	}
}
