package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
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

// ---------- the forge that cannot answer ----------

// fakeForge stands in for gh. The defect under test is entirely about what
// warden does when the forge misbehaves, so the forge has to be injectable —
// a test that arranged a real broken credential would never have been written,
// which is why this shipped.
type fakeForge struct {
	available bool
	reachable error
	answer    func(sha string) (domain.Approval, error)
	asked     []string
}

func (f *fakeForge) Available() bool                   { return f.available }
func (f *fakeForge) Reachable(_ context.Context) error { return f.reachable }
func (f *fakeForge) ApprovalFor(_ context.Context, sha string) (domain.Approval, error) {
	f.asked = append(f.asked, sha)
	return f.answer(sha)
}

func neverAsked(t *testing.T) func(string) (domain.Approval, error) {
	return func(sha string) (domain.Approval, error) {
		t.Errorf("read approval for %s after the preflight failed", sha)
		return domain.Approval{}, nil
	}
}

// THE PRIMARY REGRESSION. gh present but unable to authenticate answered
// "no pull request" for every commit, and warden published that as a finding:
// ten changes that all went through reviewed pull requests were reported as
// having bypassed pull requests entirely, exit 0, nothing on stderr.
//
// The preflight must stop the run before a single commit is read, and no
// document may be produced.
func TestCollectApprovals_RefusesWhenTheForgeCannotBeRead(t *testing.T) {
	f := &fakeForge{
		available: true,
		reachable: errors.New("gh: Bad credentials (HTTP 401)"),
		answer:    neverAsked(t),
	}
	var errOut bytes.Buffer
	ev, code := collectApprovals(context.Background(), f, sampleEvidence(t), &errOut)

	if code == 0 {
		t.Fatal("produced an evidence document from a forge that cannot answer")
	}
	if len(f.asked) != 0 {
		t.Errorf("asked the forge %d times after the preflight failed", len(f.asked))
	}
	if got := errOut.String(); !strings.Contains(got, "Bad credentials") {
		t.Errorf("stderr = %q, want the actual cause named", got)
	}
	// And nothing may be rendered from it: no approval section at all, rather
	// than a section full of zeros somebody would read as a clean result.
	if ev.Approvals().Collected != 0 {
		t.Errorf("attached approvals anyway: %+v", ev.Approvals())
	}
	var md bytes.Buffer
	printEvidenceMarkdown(&md, ev)
	if strings.Contains(md.String(), "Not associated with a pull request") {
		t.Error("rendered separation-of-duties findings from a forge that never answered")
	}
}

func TestCollectApprovals_RefusesWithoutTheCLI(t *testing.T) {
	f := &fakeForge{available: false, answer: neverAsked(t)}
	var errOut bytes.Buffer
	if _, code := collectApprovals(context.Background(), f, sampleEvidence(t), &errOut); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	// The PATH case keeps its own message: "install gh" and "fix your token"
	// are different problems with different fixes.
	if !strings.Contains(errOut.String(), "gh CLI on PATH") {
		t.Errorf("stderr = %q, want the PATH message", errOut.String())
	}
}

// The preflight cannot cover a credential that expires on commit 400 of 900.
// Those commits must land in their own bucket, not in "no pull request".
func TestCollectApprovals_MidRunFailureIsUndeterminedNotAFinding(t *testing.T) {
	f := &fakeForge{
		available: true,
		answer: func(sha string) (domain.Approval, error) {
			if strings.HasSuffix(sha, "0002") {
				return domain.Approval{Undetermined: true, Reason: "gh: API rate limit exceeded"}, nil
			}
			return domain.Approval{Found: true, PR: 1, Author: "alice", Approvers: []string{"bob"}}, nil
		},
	}
	var errOut bytes.Buffer
	ev, code := collectApprovals(context.Background(), f, sampleEvidence(t), &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want the other commits to still be reported", code)
	}

	a := ev.Approvals()
	if a.Undetermined != 1 {
		t.Errorf("undetermined = %d, want 1", a.Undetermined)
	}
	if a.NoPullRequest != 0 {
		t.Errorf("no_pull_request = %d — a rate limit was recorded as a bypassed pull request", a.NoPullRequest)
	}
	if !strings.Contains(errOut.String(), "rate limit") {
		t.Errorf("stderr = %q, want the operator told why", errOut.String())
	}
}

// The error return is reachable now (it was dead code: every path returned a
// nil error), and an error must degrade to "warden did not observe this"
// rather than aborting a 900-commit report or being counted as a finding.
func TestCollectApprovals_AnErrorFromTheForgeIsUndetermined(t *testing.T) {
	f := &fakeForge{
		available: true,
		answer: func(string) (domain.Approval, error) {
			return domain.Approval{}, errors.New("context deadline exceeded")
		},
	}
	ev, code := collectApprovals(context.Background(), f, sampleEvidence(t), io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	a := ev.Approvals()
	if a.Undetermined != a.Collected {
		t.Errorf("summary = %+v, want every erroring lookup undetermined", a)
	}
	if a.NoPullRequest != 0 {
		t.Errorf("no_pull_request = %d, want 0", a.NoPullRequest)
	}
}

// undeterminedEvidence: one change read cleanly, one the forge never answered for.
func undeterminedEvidence(t *testing.T) domain.Evidence {
	t.Helper()
	return sampleEvidence(t).WithApprovals(map[string]domain.Approval{
		"a1b2c3d4e5f60001": {Found: true, PR: 10, Author: "alice", Approvers: []string{"bob"}},
		"a1b2c3d4e5f60002": {Found: false},
		"a1b2c3d4e5f60003": {Undetermined: true, Reason: "gh: API rate limit exceeded"},
	})
}

// The undetermined count needs its own row, and the document has to say the
// table is partial — an auditor must not read a partial answer as a complete
// one.
func TestEvidenceMarkdown_UndeterminedGetsItsOwnRowAndDisclaimer(t *testing.T) {
	var out bytes.Buffer
	printEvidenceMarkdown(&out, undeterminedEvidence(t))
	md := out.String()

	if !strings.Contains(md, "| Could not be determined — the forge did not answer | 1 |") {
		t.Errorf("no undetermined row in the separation-of-duties table:\n%s", md)
	}
	if !strings.Contains(md, "| Not associated with a pull request | 1 |") {
		t.Error("the genuine no-PR finding was lost or absorbed")
	}
	if !strings.Contains(md, "This table is incomplete.") {
		t.Error("a partial answer is presented as a complete one")
	}
	// The shortfall sentence must be stated over what the forge answered, not
	// over the whole population — otherwise an outage reads as a control failure.
	if !strings.Contains(md, "1 of the 2 changes warden could read") {
		t.Errorf("the shortfall is computed over the wrong denominator:\n%s", md)
	}
	if !strings.Contains(md, "INCOMPLETE") {
		t.Error("the control text does not degrade")
	}
}

// A complete run must not carry the disclaimer, or it stops meaning anything.
func TestEvidenceMarkdown_CompleteApprovalRunCarriesNoIncompletenessClaim(t *testing.T) {
	var out bytes.Buffer
	printEvidenceMarkdown(&out, approvalEvidence(t))
	md := out.String()
	if strings.Contains(md, "This table is incomplete.") {
		t.Error("a complete run disclaimed itself")
	}
	if !strings.Contains(md, "| Could not be determined — the forge did not answer | 0 |") {
		t.Error("the undetermined row should still be present, stating zero")
	}
}

func TestEvidenceJSON_KeepsUndeterminedOutOfTheFindings(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := printEvidenceJSON(&out, &errOut, undeterminedEvidence(t)); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var doc evidenceJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Approvals == nil {
		t.Fatal("no separation_of_duties block")
	}
	if doc.Approvals.Undetermined != 1 {
		t.Errorf("undetermined = %d, want 1", doc.Approvals.Undetermined)
	}
	if doc.Approvals.NoPullRequest != 1 {
		t.Errorf("no_pull_request = %d, want the one genuine finding", doc.Approvals.NoPullRequest)
	}
	if doc.Approvals.Determined != 2 {
		t.Errorf("determined = %d, want 2", doc.Approvals.Determined)
	}
	var flagged int
	for _, c := range doc.Population {
		if c.ApprovalUnknown {
			flagged++
		}
	}
	if flagged != 1 {
		t.Errorf("population rows marked undetermined = %d, want 1 — an absent pull_request field reads as 'there was none'", flagged)
	}
}

func TestEvidenceOSCAL_SaysWhenApprovalCouldNotBeRead(t *testing.T) {
	e := sampleEvidence(t).WithApprovals(map[string]domain.Approval{
		// The gated commit is the only one rendered as an observation.
		"a1b2c3d4e5f60001": {Undetermined: true, Reason: "gh: API rate limit exceeded"},
	})
	var out, errOut bytes.Buffer
	if code := printEvidenceOSCAL(&out, &errOut, e); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	body := out.String()
	if !strings.Contains(body, "could not read the forge's approval record") {
		t.Errorf("the observation does not disclose that approval is unknown:\n%s", body)
	}
	if strings.Contains(body, "no approving review") {
		t.Error("an unreadable record was described as a change nobody approved")
	}
}

// ---------- repository identity ----------

// The evidence document leaves the organization. A local filesystem path is
// not a repository identity — two clones produce different labels, an auditor
// cannot resolve one, and it discloses the producer's home directory.
func TestRepoLabel_NamesTheRemoteNotTheWorkingDirectory(t *testing.T) {
	got := repoLabel("https://github.com/klarlabs-studio/warden.git")
	if got != "https://github.com/klarlabs-studio/warden.git" {
		t.Errorf("repoLabel = %q, want the remote", got)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, wd) {
		t.Errorf("repoLabel leaked the working directory: %q", got)
	}
}

// A local-only repository still has to produce a valid document — but never by
// falling back to the absolute path.
func TestRepoLabel_LocalOnlyRepoSaysSoRatherThanNamingAPath(t *testing.T) {
	got := repoLabel("")
	if got == "" {
		t.Fatal("empty label")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, wd) || strings.HasPrefix(got, "/") {
		t.Errorf("repoLabel = %q, want an identifier rather than a filesystem path", got)
	}
	if !strings.Contains(got, "no origin remote") {
		t.Errorf("repoLabel = %q, want it to say why there is no name", got)
	}
}

// A CI checkout's origin often carries a token. This string is rendered into
// the document header and into the OSCAL title a GRC platform ingests.
//
// The hosts here are deliberately dotless, the same convention TestNormalizeRemote
// uses for the same reason: a `user:secret@host` literal is what a credential
// scanner exists to find, and warden's own security-scan step reads this file.
// The syntax under test is unaffected — the parser never looks at the host — and
// rewording beats baselining a finding that would then read as an unexplained
// hash to whoever prunes the baseline later.
func TestRepoLabel_StripsCredentialsFromTheRemote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x-access-token:redacted@githost/o/r.git", "https://githost/o/r.git"},
		{"https://user@githost/o/r.git", "https://githost/o/r.git"},
		{"git@githost:o/r.git", "git@githost:o/r.git"},
		{"ssh://git@githost/o/r.git", "ssh://githost/o/r.git"},
		{"https://githost/o/r.git", "https://githost/o/r.git"},
		{"  https://githost/o/r.git  ", "https://githost/o/r.git"},
	}
	for _, c := range cases {
		if got := repoLabel(c.in); got != c.want {
			t.Errorf("repoLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The digest is deliberately clone-stable: it covers the commits and their
// verdicts, not the label. Changing how the repository is named must not
// change what the digest fingerprints.
func TestEvidence_DigestIgnoresTheRepositoryLabel(t *testing.T) {
	a := sampleEvidence(t)
	b := a
	b.Repository = "https://github.com/klarlabs-studio/warden.git"
	if a.Digest() != b.Digest() {
		t.Error("the repository label leaked into the evidence digest")
	}
}
