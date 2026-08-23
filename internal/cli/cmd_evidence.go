package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	forgepkg "go.klarlabs.de/warden/internal/infrastructure/forge"
)

// cmdEvidence renders a period-scoped, control-mapped evidence package.
//
// `warden audit` answers a developer's question — is this branch's provenance
// intact. This answers an auditor's: over this window, what changed, which
// changes were gated, and what are the exceptions. Same underlying data, and
// deliberately so; two commands that could disagree about what happened would
// make both useless.
//
// Three formats because the artifact has three readers. `md` for the human who
// signs the opinion, `json` for the GRC platform that holds it, `oscal` for
// tooling that speaks the NIST interchange format.
func cmdEvidence(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branchFlag := fs.String("branch", "", "branch to report on (default: current)")
	formatFlag := fs.String("format", "md", "output format: md | json | oscal")
	fromFlag := fs.String("from", "", "start of the period, YYYY-MM-DD (default: adoption)")
	toFlag := fs.String("to", "", "end of the period, YYYY-MM-DD (default: now)")
	frameworksFlag := fs.String("frameworks", "", "comma-separated: soc2, iso27001 (default: all)")
	approvalsFlag := fs.Bool("approvals", false, "also read who approved each change from the forge (one API call per commit)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "evidence", "branch") {
		return 2
	}

	switch *formatFlag {
	case "md", "json", "oscal":
	default:
		_, _ = fmt.Fprintf(stderr, "warden: unknown --format %q (want md, json, or oscal)\n", *formatFlag)
		return 2
	}

	from, err := parsePeriodBound(*fromFlag, false)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: --from: %v\n", err)
		return 2
	}
	to, err := parsePeriodBound(*toFlag, true)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: --to: %v\n", err)
		return 2
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		_, _ = fmt.Fprintf(stderr, "warden: --to %s is before --from %s\n", *toFlag, *fromFlag)
		return 2
	}

	// A typo in a framework name must not silently produce a thinner report
	// than the one somebody thinks they asked for.
	frameworks := splitFrameworks(*frameworksFlag)
	for _, f := range frameworks {
		if !known(f) {
			_, _ = fmt.Fprintf(stderr, "warden: unknown framework %q (have %s)\n",
				f, strings.Join(domain.KnownFrameworks(), ", "))
			return 2
		}
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	report, err := svc.Audit(*branchFlag)
	if err != nil {
		return fail(stderr, err)
	}

	var remote string
	if repo := svc.Repo(); repo != nil {
		remote = repo.RemoteURL("origin")
	}

	ev := domain.NewEvidence(report, domain.EvidenceOptions{
		Tool:       "warden " + Version,
		Repository: repoLabel(remote),
		From:       from,
		To:         to,
		Now:        time.Now().UTC(),
		Frameworks: frameworks,
	})

	// Opt-in: one forge call per commit is slow and rate-limited, and a report
	// that silently took four minutes is a report nobody runs twice.
	if *approvalsFlag {
		var code int
		if ev, code = collectApprovals(context.Background(), forgepkg.NewGH(mustCwd()), ev, stderr); code != 0 {
			return code
		}
	}

	switch *formatFlag {
	case "json":
		return printEvidenceJSON(stdout, stderr, ev)
	case "oscal":
		return printEvidenceOSCAL(stdout, stderr, ev)
	default:
		printEvidenceMarkdown(stdout, ev)
	}
	return 0
}

// approvalForge is the slice of the forge this command needs. It is an
// interface so the failure modes below can be exercised without a real gh —
// the whole point of this code is what happens when the forge misbehaves, and
// a test that has to arrange a broken credential would never be written.
type approvalForge interface {
	Available() bool
	Reachable(ctx context.Context) error
	ApprovalFor(ctx context.Context, sha string) (domain.Approval, error)
}

// collectApprovals attaches the forge's review records to the evidence, or
// refuses to produce a document at all.
//
// The refusal is the important half. A gh that is present but cannot answer
// returns "no pull request" for every commit, and the rendered document then
// asserts, as fact, that every change in the period bypassed pull requests.
// That is a fabricated compliance finding in a signed artifact, and it is
// strictly worse than no artifact: nobody looks again at a report they already
// have. So the forge is proven reachable BEFORE the first commit is read, and
// a failure there stops the command.
//
// The preflight cannot cover everything — a credential can expire, or a rate
// limit can bite, on commit 400 of 900 — so per-commit uncertainty is carried
// through as its own state rather than being collapsed into a finding.
// Both refusals below exit 2, not 75/78, and the distinction is worth stating
// because 78 looks like the better fit: an absent gh IS a missing toolchain and
// an expired credential IS the machine rather than the change.
//
// 75 and 78 exist so a wrapper around the GATE can tell "warden rejected your
// code" from "warden never got to judge it" — the whole point is that a verdict
// on the change was expected and did not arrive. `warden evidence` judges no
// change and returns no verdict; it is a reporting command, and borrowing the
// gate's vocabulary here would tell a caller a verdict was withheld when none
// was ever due. 2 says what actually happened: warden could not do the thing it
// was asked to do.
func collectApprovals(ctx context.Context, f approvalForge, ev domain.Evidence, stderr io.Writer) (withApprovals domain.Evidence, exitCode int) {
	if !f.Available() {
		_, _ = fmt.Fprintln(stderr, "warden: --approvals needs the gh CLI on PATH")
		return ev, 2
	}
	if err := f.Reachable(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: --approvals cannot read the forge: %v\n", err)
		_, _ = fmt.Fprintln(stderr, "warden: refusing to write evidence — a forge that cannot answer would be recorded as a forge with no pull requests, and every change would be reported as having bypassed review")
		return ev, 2
	}

	byCommit := make(map[string]domain.Approval, len(ev.Population))
	var undetermined int
	var firstReason string
	for i := range ev.Population {
		sha := ev.Population[i].SHA
		a, err := f.ApprovalFor(ctx, sha)
		if err != nil {
			// The forge adapter reports its own failures as an undetermined
			// Approval; an error here is something it could not classify at all.
			// Either way the honest record is "warden did not observe this".
			a = domain.Approval{Undetermined: true, Reason: err.Error()}
		}
		if a.Undetermined {
			undetermined++
			if firstReason == "" {
				firstReason = short(sha) + ": " + a.Reason
			}
		}
		byCommit[sha] = a
	}
	if undetermined > 0 {
		// On stderr, not in the document: the document states the COUNT, which
		// is what bounds the claim. The cause is for whoever re-runs it.
		_, _ = fmt.Fprintf(stderr, "warden: approval undetermined for %d of %d changes (first %s)\n",
			undetermined, len(byCommit), firstReason)
	}
	return ev.WithApprovals(byCommit), 0
}

// repoLabel names the repository the evidence is about.
//
// The remote URL, not the working directory. An evidence document leaves the
// organization, and a local path is not a repository identity: two clones of
// the same repository produce different labels, an auditor cannot resolve one
// to anything, and it discloses the producer's filesystem. Same reasoning as
// deriving the adoption point from the shared notes ref — an artifact only one
// laptop can produce is not evidence.
//
// A local-only repository still produces a valid document, identified as such
// rather than by a path that means nothing outside this machine.
func repoLabel(remote string) string {
	if r := sanitizeRemote(remote); r != "" {
		return r
	}
	return "(local repository — no origin remote)"
}

// sanitizeRemote strips credentials from a remote URL. A CI checkout's origin
// is often https://x-access-token:<token>@github.com/owner/repo, and this
// string is rendered into the document header and into the OSCAL title that a
// GRC platform ingests.
//
// ALL userinfo goes, not just the password half: a token is as often carried
// as the username (https://ghp_…@github.com/…) as after a colon. What
// identifies the repository is the host and path, and both survive intact.
func sanitizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	scheme := strings.Index(remote, "://")
	if scheme < 0 {
		return remote // scp-style (git@host:owner/repo) carries no secret
	}
	rest := remote[scheme+3:]
	at := strings.LastIndex(rest, "@")
	slash := strings.IndexByte(rest, '/')
	if at >= 0 && (slash < 0 || at < slash) {
		rest = rest[at+1:]
	}
	return remote[:scheme+3] + rest
}

func known(f string) bool {
	for _, k := range domain.KnownFrameworks() {
		if k == f {
			return true
		}
	}
	return false
}

func splitFrameworks(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePeriodBound reads YYYY-MM-DD. The end bound covers its whole day —
// `--to 2026-03-31` that silently excluded most of the 31st would drop commits
// from a population an auditor reconciles by hand.
func parsePeriodBound(s string, end bool) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("want YYYY-MM-DD, got %q", s)
	}
	if end {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t, nil
}

// ---------- Markdown: the human who signs the opinion ----------

func printEvidenceMarkdown(w io.Writer, e domain.Evidence) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	p("# Change-gate evidence\n\n")
	p("| | |\n|---|---|\n")
	p("| Repository | `%s` |\n", e.Repository)
	p("| Branch | `%s` |\n", e.Branch)
	p("| Period | %s |\n", periodLabel(e))
	p("| Control in operation since | `%s` |\n", shortOrNone(e.Adoption))
	p("| Produced by | %s at %s |\n", e.Tool, e.GeneratedAt.Format(time.RFC3339))
	p("| Evidence digest | `%s` |\n", e.Digest())
	p("\n")

	t := e.Tally
	p("## Population\n\n")
	p("Every change on the branch in the period, classified. Not a sample.\n\n")
	p("| Classification | Count | Meaning |\n|---|---:|---|\n")
	p("| Gated and verified | %d | Checks ran and passed against this exact tree; record signed and bound to the commit |\n", t.Verified)
	p("| Covered by a gated push | %d | Published inside a signed push span — gated as a unit with the span's tip |\n", t.Covered)
	p("| Exceptions | %d | warden could not vouch for these; enumerated below |\n", t.Unverified)
	p("| Outside the control | %d | Predates adoption, or otherwise unattributable |\n", t.Unknown)
	p("| **Total** | **%d** | |\n\n", t.Total())

	ex := e.Exceptions()
	p("## Exceptions\n\n")
	if len(ex) == 0 {
		p("None. Every change in the period was gated or covered by a gated push.\n\n")
	} else {
		p("| Commit | Change | Date | Author | Reason | Remediation |\n|---|---|---|---|---|---|\n")
		for _, x := range ex {
			p("| `%s` | %s | %s | %s | %s | %s |\n",
				short(x.SHA), x.Subject, x.Date, x.Author, x.Reason, orDash(x.Remediation))
		}
		p("\n")
	}

	if a := e.Approvals(); a.Collected > 0 {
		p("## Separation of duties\n\n")
		p("Who approved each change, as recorded by the forge. An approval by the\n")
		p("author is reported as a self-approval and does not count as review.\n\n")
		p("| | Count |\n|---|---:|\n")
		p("| Approved by someone other than the author | %d |\n", a.Independent)
		p("| Self-approved only | %d |\n", a.SelfApprovedOnly)
		p("| Merged through a pull request nobody approved | %d |\n", a.Unapproved)
		p("| Not associated with a pull request | %d |\n", a.NoPullRequest)
		// Its own row, always, and never folded into the row above it. A change
		// warden could not look up is not a change that bypassed review, and the
		// two rows summing to the population is what lets an auditor see that.
		p("| Could not be determined — the forge did not answer | %d |\n", a.Undetermined)
		p("| **Total** | **%d** |\n\n", a.Collected)

		if a.Undetermined > 0 {
			p("**This table is incomplete.** warden could not read the forge's record\n")
			p("for %d of the %d changes in the period, so the counts above describe %d\n",
				a.Undetermined, a.Collected, a.Determined())
			p("changes, not %d. The undetermined changes are not evidence of anything:\n", a.Collected)
			p("they are not approved, not unapproved, and not known to have bypassed a\n")
			p("pull request. Re-run the report once the forge can be read to close them.\n\n")
		}
		if a.Independent < a.Determined() {
			p("%d of the %d changes warden could read did not carry an independent approval.\n",
				a.Determined()-a.Independent, a.Determined())
			p("Where that is expected — a single-maintainer repository, an automated\n")
			p("dependency bump — say so in the compensating-control narrative rather\n")
			p("than leaving an auditor to infer it.\n\n")
		}
	}

	p("## Controls\n\n")
	for _, c := range e.Controls {
		p("### %s %s — %s\n\n", c.Framework, c.ID, c.Name)
		p("**Evidenced.** %s\n\n", c.Evidences)
		p("**Not evidenced.** %s\n\n", c.Limits)
	}

	p("## What this evidence does and does not support\n\n")
	p("Supported:\n\n")
	for _, s := range e.Assertions.Supported {
		p("- %s\n", s)
	}
	p("\nNot supported:\n\n")
	for _, s := range e.Assertions.Unsupported {
		p("- %s\n", s)
	}

	p("\n## Independent verification\n\n")
	p("This report asserts the verdicts; it is not the proof of them. The proof is\n")
	p("the signed records in the repository, which can be re-checked without this\n")
	p("report and without trusting whoever produced it:\n\n")
	p("```\n%s\n```\n\n", e.VerifyCommand())
	p("Re-running this report over the same period must reproduce the evidence\n")
	p("digest above. A differing digest means the underlying records changed.\n")
}

func periodLabel(e domain.Evidence) string {
	switch {
	case e.From.IsZero() && e.To.IsZero():
		return "all history since adoption"
	case e.From.IsZero():
		return "up to " + e.To.Format("2006-01-02")
	case e.To.IsZero():
		return "from " + e.From.Format("2006-01-02")
	}
	return e.From.Format("2006-01-02") + " to " + e.To.Format("2006-01-02")
}

// shortOrNone abbreviates a SHA, saying so when there is none — an empty cell
// in an evidence table reads as an omission.
func shortOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return short(s)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------- JSON: the GRC platform that holds it ----------

// evidenceSchema versions the JSON contract. A platform ingesting this on a
// schedule needs to know when the shape changes without diffing it.
const evidenceSchema = "warden.evidence/v1"

type approvalJSON struct {
	Collected        int `json:"collected"`
	Determined       int `json:"determined"`
	Independent      int `json:"independent"`
	SelfApprovedOnly int `json:"self_approved_only"`
	Unapproved       int `json:"unapproved"`
	NoPullRequest    int `json:"no_pull_request"`
	// Undetermined is the forge lookups that did not return an answer. A
	// platform must subtract these before computing a rate; folding them into
	// no_pull_request would render a forge outage as a control failure.
	Undetermined int `json:"undetermined"`
}

type evidenceJSON struct {
	Schema     string           `json:"schema"`
	Tool       string           `json:"tool"`
	Repository string           `json:"repository"`
	Branch     string           `json:"branch"`
	Adoption   string           `json:"adoption_commit"`
	Period     periodJSON       `json:"period"`
	Generated  string           `json:"generated_at"`
	Digest     string           `json:"evidence_digest"`
	Verify     string           `json:"verification_command"`
	Summary    summaryJSON      `json:"summary"`
	Population []populationJSON `json:"population"`
	Exceptions []exceptionJSON  `json:"exceptions"`
	Approvals  *approvalJSON    `json:"separation_of_duties,omitempty"`
	Controls   []controlJSON    `json:"controls"`
	Assertions assertionsJSON   `json:"assertions"`
}

type periodJSON struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type summaryJSON struct {
	Total      int `json:"total"`
	Verified   int `json:"gated_and_verified"`
	Covered    int `json:"covered_by_gated_push"`
	Exceptions int `json:"exceptions"`
	Outside    int `json:"outside_control"`
}

type populationJSON struct {
	SHA       string   `json:"sha"`
	Date      string   `json:"date"`
	Author    string   `json:"author"`
	Subject   string   `json:"subject"`
	State     string   `json:"state"`
	Steps     []string `json:"steps,omitempty"`
	RunID     string   `json:"run_id,omitempty"`
	CoveredBy string   `json:"covered_by,omitempty"`
	PR        int      `json:"pull_request,omitempty"`
	PRAuthor  string   `json:"pull_request_author,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
	// ApprovalUnknown marks a row whose forge record could not be read. Without
	// it, an absent pull_request field reads as "there was no pull request".
	ApprovalUnknown bool   `json:"approval_undetermined,omitempty"`
	Explanation     string `json:"explanation,omitempty"`
}

type exceptionJSON struct {
	SHA         string `json:"sha"`
	Date        string `json:"date"`
	Author      string `json:"author"`
	Subject     string `json:"subject"`
	Reason      string `json:"reason"`
	Remediation string `json:"remediation,omitempty"`
}

type controlJSON struct {
	Framework string `json:"framework"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Evidenced string `json:"evidenced"`
	Limits    string `json:"not_evidenced"`
}

type assertionsJSON struct {
	Supported   []string `json:"supported"`
	Unsupported []string `json:"unsupported"`
}

func printEvidenceJSON(stdout, stderr io.Writer, e domain.Evidence) int {
	doc := evidenceJSON{
		Schema:     evidenceSchema,
		Tool:       e.Tool,
		Repository: e.Repository,
		Branch:     e.Branch,
		Adoption:   e.Adoption,
		Period:     periodJSON{From: dateOrEmpty(e.From), To: dateOrEmpty(e.To)},
		Generated:  e.GeneratedAt.Format(time.RFC3339),
		Digest:     e.Digest(),
		Verify:     e.VerifyCommand(),
		Summary: summaryJSON{
			Total: e.Tally.Total(), Verified: e.Tally.Verified, Covered: e.Tally.Covered,
			Exceptions: e.Tally.Unverified, Outside: e.Tally.Unknown,
		},
		Population: make([]populationJSON, 0, len(e.Population)),
		Exceptions: []exceptionJSON{},
		Controls:   make([]controlJSON, 0, len(e.Controls)),
		Assertions: assertionsJSON{
			Supported:   e.Assertions.Supported,
			Unsupported: e.Assertions.Unsupported,
		},
	}
	for i := range e.Population {
		c := &e.Population[i]
		steps := make([]string, 0, len(c.Steps))
		for _, s := range c.Steps {
			steps = append(steps, string(s))
		}
		row := populationJSON{
			SHA: c.SHA, Date: c.Date, Author: c.Author, Subject: c.Subject,
			State: stateOf(c), Steps: steps, RunID: c.RunID, CoveredBy: c.CoveredBy,
		}
		if a, ok := e.ApprovalByCommit[c.SHA]; ok {
			if a.Found {
				row.PR, row.PRAuthor = a.PR, a.Author
			}
			// Approvers only when the lookup actually completed: an empty list on
			// an undetermined row would be read as "nobody approved".
			if a.Undetermined {
				row.ApprovalUnknown = true
			} else {
				row.Approvers = a.Approvers
			}
		}
		doc.Population = append(doc.Population, row)
	}
	for _, x := range e.Exceptions() {
		doc.Exceptions = append(doc.Exceptions, exceptionJSON{
			SHA: x.SHA, Date: x.Date, Author: x.Author, Subject: x.Subject,
			Reason: x.Reason, Remediation: x.Remediation,
		})
	}
	for _, c := range e.Controls {
		doc.Controls = append(doc.Controls, controlJSON{
			Framework: c.Framework, ID: c.ID, Name: c.Name,
			Evidenced: c.Evidences, Limits: c.Limits,
		})
	}

	if a := e.Approvals(); a.Collected > 0 {
		doc.Approvals = &approvalJSON{
			Collected: a.Collected, Determined: a.Determined(), Independent: a.Independent,
			SelfApprovedOnly: a.SelfApprovedOnly, Unapproved: a.Unapproved,
			NoPullRequest: a.NoPullRequest, Undetermined: a.Undetermined,
		}
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func stateOf(c *domain.CommitStatus) string {
	switch {
	case c.ChainIntact:
		return "verified"
	case c.CoveredBy != "":
		return "covered"
	case c.NoRemoteRef, c.PreSpanProvenance:
		return "outside_control"
	default:
		return "exception"
	}
}

func dateOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// ---------- OSCAL: the interchange format ----------

// OSCAL assessment-results, the NIST format for "here is what the assessment
// found". warden is the assessor and each commit is an observation; a commit
// warden could not vouch for is a finding.
//
// UUIDs are derived from content rather than randomly generated, so the same
// evidence produces the same document. A report whose identifiers churned on
// every run could not be diffed, and diffing consecutive periods is most of
// what a continuous-monitoring program does with these.
const oscalVersion = "1.1.2"

type oscalDoc struct {
	AssessmentResults oscalResults `json:"assessment-results"`
}

type oscalResults struct {
	UUID     string        `json:"uuid"`
	Metadata oscalMetadata `json:"metadata"`
	ImportAP oscalImportAP `json:"import-ap"`
	Results  []oscalResult `json:"results"`
}

type oscalMetadata struct {
	Title        string `json:"title"`
	LastModified string `json:"last-modified"`
	Version      string `json:"version"`
	OSCALVersion string `json:"oscal-version"`
}

type oscalImportAP struct {
	Href    string `json:"href"`
	Remarks string `json:"remarks,omitempty"`
}

type oscalResult struct {
	UUID             string             `json:"uuid"`
	Title            string             `json:"title"`
	Description      string             `json:"description"`
	Start            string             `json:"start"`
	End              string             `json:"end,omitempty"`
	ReviewedControls oscalReviewed      `json:"reviewed-controls"`
	Observations     []oscalObservation `json:"observations,omitempty"`
	Findings         []oscalFinding     `json:"findings,omitempty"`
	Remarks          string             `json:"remarks,omitempty"`
}

type oscalReviewed struct {
	ControlSelections []oscalSelection `json:"control-selections"`
}

type oscalSelection struct {
	IncludeControls []oscalControlID `json:"include-controls"`
	Remarks         string           `json:"remarks,omitempty"`
}

type oscalControlID struct {
	ControlID string `json:"control-id"`
}

type oscalObservation struct {
	UUID        string   `json:"uuid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Methods     []string `json:"methods"`
	Collected   string   `json:"collected"`
}

type oscalFinding struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Remarks     string `json:"remarks,omitempty"`
}

func printEvidenceOSCAL(stdout, stderr io.Writer, e domain.Evidence) int {
	start := e.From
	if start.IsZero() {
		start = e.GeneratedAt
	}

	sel := oscalSelection{
		Remarks: "Controls this evidence speaks to. Each control's limits are recorded in the result remarks; warden evidences the machine-checked gate only.",
	}
	for _, c := range e.Controls {
		sel.IncludeControls = append(sel.IncludeControls, oscalControlID{
			ControlID: oscalControlID2(c),
		})
	}

	res := oscalResult{
		UUID:  derivedUUID("result", e.Digest()),
		Title: "Source change-gate evidence",
		Description: fmt.Sprintf(
			"Population of %d source changes on branch %s of %s for the period %s. "+
				"%d gated and verified, %d covered by a gated push, %d exceptions, %d outside the control.",
			e.Tally.Total(), e.Branch, e.Repository, periodLabel(e),
			e.Tally.Verified, e.Tally.Covered, e.Tally.Unverified, e.Tally.Unknown),
		Start:            start.Format(time.RFC3339),
		ReviewedControls: oscalReviewed{ControlSelections: []oscalSelection{sel}},
		Remarks:          oscalRemarks(e),
	}
	if !e.To.IsZero() {
		res.End = e.To.Format(time.RFC3339)
	}

	for i := range e.Population {
		c := &e.Population[i]
		if !c.ChainIntact && c.CoveredBy == "" {
			continue // an unvouched commit is a finding, below, not an observation
		}
		steps := make([]string, 0, len(c.Steps))
		for _, s := range c.Steps {
			steps = append(steps, string(s))
		}
		desc := fmt.Sprintf("Commit %s (%s) passed the configured gate before publication.", short(c.SHA), c.Subject)
		if len(steps) > 0 {
			desc += " Steps: " + strings.Join(steps, ", ") + "."
		}
		if c.CoveredBy != "" {
			desc += " Published inside the signed push span of " + short(c.CoveredBy) + "."
		}
		if a, ok := e.ApprovalByCommit[c.SHA]; ok && a.Undetermined {
			desc += " warden could not read the forge's approval record for this change; no approval claim is made about it."
		} else if ok && a.Found {
			switch {
			case a.Independent():
				desc += fmt.Sprintf(" Arrived through PR #%d (opened by %s), approved by %s.",
					a.PR, a.Author, strings.Join(a.Approvers, ", "))
			case len(a.Approvers) > 0:
				desc += fmt.Sprintf(" Arrived through PR #%d, self-approved by %s — no independent review.", a.PR, a.Author)
			default:
				desc += fmt.Sprintf(" Arrived through PR #%d (opened by %s) with no approving review.", a.PR, a.Author)
			}
		}
		res.Observations = append(res.Observations, oscalObservation{
			UUID:        derivedUUID("observation", c.SHA),
			Title:       "Gated change " + short(c.SHA),
			Description: desc,
			Methods:     []string{"TEST"},
			Collected:   c.Date,
		})
	}

	for _, x := range e.Exceptions() {
		f := oscalFinding{
			UUID:        derivedUUID("finding", x.SHA),
			Title:       "Change without verifiable gate record: " + short(x.SHA),
			Description: fmt.Sprintf("Commit %s (%s), authored by %s on %s: %s", short(x.SHA), x.Subject, x.Author, x.Date, x.Reason),
		}
		if x.Remediation != "" {
			f.Remarks = "Remediation: " + x.Remediation
		}
		res.Findings = append(res.Findings, f)
	}

	doc := oscalDoc{AssessmentResults: oscalResults{
		UUID: derivedUUID("assessment", e.Digest()+e.Branch),
		Metadata: oscalMetadata{
			Title:        "warden change-gate evidence — " + e.Repository,
			LastModified: e.GeneratedAt.Format(time.RFC3339),
			Version:      e.Digest()[:12],
			OSCALVersion: oscalVersion,
		},
		ImportAP: oscalImportAP{
			Href:    "#assessment-plan-not-provided",
			Remarks: "warden produces assessment results from a continuously-operating control; there is no separate assessment plan document.",
		},
		Results: []oscalResult{res},
	}}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// oscalControlID2 renders a framework control as an OSCAL control-id. SOC 2
// has no OSCAL catalog, so the id is namespaced rather than pretending to
// reference one: a consumer can map it, and cannot mistake it for NIST 800-53.
func oscalControlID2(c domain.Control) string {
	ns := strings.ToLower(c.Framework)
	ns = strings.NewReplacer(" ", "-", "/", "-", ":", "-").Replace(ns)
	return ns + "-" + strings.ToLower(c.ID)
}

func oscalRemarks(e domain.Evidence) string {
	var b strings.Builder
	b.WriteString("Evidence digest: " + e.Digest() + ". Independently verifiable with: " + e.VerifyCommand() + ".\n\n")
	b.WriteString("This assessment covers the machine-checked source gate only. It does NOT evidence:\n")
	for _, u := range e.Assertions.Unsupported {
		b.WriteString("  - " + u + "\n")
	}
	b.WriteString("\nPer-control limits:\n")
	for _, c := range e.Controls {
		b.WriteString("  - " + c.Framework + " " + c.ID + ": " + c.Limits + "\n")
	}
	return b.String()
}

// derivedUUID makes a stable RFC-4122-shaped identifier out of content. It is
// a digest in UUID clothing, version nibble set to 4 so consumers that
// validate the format accept it; nothing reads it as a random UUID.
func derivedUUID(kind, content string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + content))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s-%s-4%s-a%s-%s", h[0:8], h[8:12], h[13:16], h[17:20], h[20:32])
}
