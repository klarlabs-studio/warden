package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Evidence is a period-scoped, control-mapped view of an audit, shaped for
// somebody who is not a developer: an auditor sampling a quarter, or a GRC
// platform holding the evidence for one.
//
// It adds three things to an AuditReport, and deliberately nothing else.
//
//  1. A PERIOD. An audit runs over a branch's whole history since adoption; an
//     audit engagement covers a window. Evidence outside the window is noise
//     that invites questions the engagement is not asking.
//
//  2. A POPULATION, not a sample. The auditor's own words for what they want:
//     every change in the window, classified, with the exceptions explained.
//     AuditReport's partition already does this — Verified, Covered,
//     Unverified, Unknown account for every commit exactly once — which is why
//     this type is a projection and not a second implementation.
//
//  3. CONTROL MAPPING with its limits stated. See Control.Limits: the reason
//     evidence gets rejected is almost never that it proves too little, it is
//     that it claims more than it proves and the auditor stops trusting the
//     rest of it.
type Evidence struct {
	// Tool is the producing binary and version, so a report can be reproduced.
	Tool string
	// Repository is the remote URL or local path the audit ran against.
	Repository string
	Branch     string
	// Adoption is the commit warden started gating at. Commits before it are
	// outside any claim: the control did not exist yet.
	Adoption string

	From, To    time.Time
	GeneratedAt time.Time

	// Population is every commit in the window, in the audit's order.
	Population []CommitStatus
	Tally      Tally

	Controls   []Control
	Assertions Assertions

	// ApprovalByCommit is the forge's review record per commit, populated only
	// when the report was asked to collect it (it costs a forge call per
	// commit). Empty means "not collected", which the renderers must say rather
	// than presenting as "no approvals found".
	ApprovalByCommit map[string]Approval
}

// Assertions separates what the evidence supports from what it does not.
//
// The second list is the load-bearing one. A gate can prove that named checks
// ran and passed against an exact tree, signed by a key. It cannot prove the
// checks were adequate, that a second person looked, or that what runs in
// production is this commit. An evidence package that quietly implies
// otherwise is worse than no package: it moves the argument from "here is what
// we have" to "why did you say this".
type Assertions struct {
	Supported   []string
	Unsupported []string
}

// Control is one framework control this evidence speaks to.
type Control struct {
	// Framework is the scheme, e.g. "SOC 2" or "ISO/IEC 27001:2022".
	Framework string
	// ID is the control identifier as the framework writes it, e.g. "CC8.1".
	ID string
	// Name is the framework's own short name for the control.
	Name string
	// Evidences is what warden's data actually shows for this control.
	Evidences string
	// Limits is what it does NOT show, in the same breath. Never empty: a
	// control warden fully satisfied on its own would be a control about
	// warden, not about the organization.
	Limits string
}

// EvidenceOptions parameterises a report.
type EvidenceOptions struct {
	Tool       string
	Repository string
	From, To   time.Time
	Now        time.Time
	// Frameworks selects control catalogs by key ("soc2", "iso27001").
	// Empty means every catalog warden knows.
	Frameworks []string
}

// NewEvidence projects an audit into a period-scoped evidence package.
func NewEvidence(report AuditReport, opts EvidenceOptions) Evidence {
	pop := make([]CommitStatus, 0, len(report.Commits))
	for i := range report.Commits {
		if inPeriod(report.Commits[i].Date, opts.From, opts.To) {
			pop = append(pop, report.Commits[i])
		}
	}

	return Evidence{
		Tool:        opts.Tool,
		Repository:  opts.Repository,
		Branch:      report.Branch,
		Adoption:    report.Adoption,
		From:        opts.From,
		To:          opts.To,
		GeneratedAt: opts.Now,
		Population:  pop,
		Tally:       AuditReport{Branch: report.Branch, Adoption: report.Adoption, NeverPushed: report.NeverPushed, Commits: pop}.Counts(),
		Controls:    ControlsFor(opts.Frameworks),
		Assertions:  standardAssertions,
	}
}

// inPeriod reports whether a commit date falls in [from, to]. A zero bound is
// open, so a report with neither is the whole history — the same population
// `warden audit` shows, which keeps the two commands consistent.
//
// An unparseable date is INCLUDED. Dropping a commit because its timestamp did
// not parse would silently shrink the population, and a population an auditor
// cannot reconcile against `git log` is the one thing this must never produce.
func inPeriod(date string, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return true
	}
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// Exceptions are the commits an auditor will ask about: every commit warden
// could not vouch for, with the reason it could not.
//
// Verified and Covered commits are not exceptions. Everything else is, and
// each carries the most specific reason the audit could determine rather than
// a bare "unverified" — an exception list where every row says the same thing
// is a list nobody can act on.
func (e Evidence) Exceptions() []Exception {
	var out []Exception
	for i := range e.Population {
		c := &e.Population[i]
		if c.ChainIntact || c.CoveredBy != "" {
			continue
		}
		out = append(out, Exception{
			SHA:     c.SHA,
			Date:    c.Date,
			Author:  c.Author,
			Subject: c.Subject,
			Reason:  exceptionReason(c),
			// A remediable exception is one where the content demonstrably was
			// gated and only the binding is missing. Saying so is the
			// difference between a finding and a to-do.
			Remediation: exceptionRemedy(c),
		})
	}
	return out
}

// Exception is one commit warden could not vouch for.
type Exception struct {
	SHA         string
	Date        string
	Author      string
	Subject     string
	Reason      string
	Remediation string
}

func exceptionReason(c *CommitStatus) string {
	switch {
	case c.NoRemoteRef:
		return "never pushed — the pre-push gate was never reachable for this commit"
	case c.PreSpanProvenance:
		return "unattributable — gated by a warden too old to record a push span, so a gated intermediate commit and a bypass are indistinguishable"
	case c.HasNote && c.NoteDefect != "":
		return "note present but does not attest the commit: " + c.NoteDefect
	case c.ReattestableFrom != "":
		return "no note of its own; a content-identical validated commit exists (squash-merge binding gap)"
	case !c.HasNote:
		return "no warden note — pushed with --no-verify, or committed outside warden"
	default:
		return "unverified"
	}
}

func exceptionRemedy(c *CommitStatus) string {
	switch {
	case c.ReattestableFrom != "":
		return "warden reattest --commit " + shortSHA(c.SHA) + " --push"
	case c.NoRemoteRef:
		return "none needed unless the branch is pushed"
	default:
		return ""
	}
}

// Digest fingerprints the population so a report can be shown to be the one
// that was generated, and regenerated to the same value from the same repo.
//
// It covers the commits and their verdicts, not the timestamps around them: a
// report re-run tomorrow over the same window must produce the same digest, or
// nobody can tell an edited report from a re-run one.
func (e Evidence) Digest() string {
	h := sha256.New()
	rows := make([]string, 0, len(e.Population))
	for i := range e.Population {
		c := &e.Population[i]
		steps := make([]string, 0, len(c.Steps))
		for _, s := range c.Steps {
			steps = append(steps, string(s))
		}
		rows = append(rows, fmt.Sprintf("%s|%t|%t|%s|%s|%s",
			c.SHA, c.HasNote, c.ChainIntact, c.CoveredBy, c.NoteDefect, strings.Join(steps, ",")))
	}
	sort.Strings(rows)
	for _, r := range rows {
		_, _ = fmt.Fprintln(h, r)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyCommand is what an auditor runs to re-check the evidence themselves,
// rather than trusting the report that asserts it.
func (e Evidence) VerifyCommand() string {
	if e.Adoption == "" {
		return "warden verify --range <base>.." + e.Branch
	}
	return "warden verify --range " + shortSHA(e.Adoption) + ".." + e.Branch
}

// standardAssertions is deliberately a constant, not a per-run computation.
// What a gate can and cannot prove is a property of the mechanism, and a
// report whose disclaimers varied with its contents would be a report whose
// disclaimers were negotiable.
var standardAssertions = Assertions{
	Supported: []string{
		"The named checks ran to completion and passed against the exact source tree of each verified commit, before that commit reached the remote.",
		"Each verified commit's record is bound to its commit id and hash-chained across steps, so a record cannot be moved to another commit or have a step removed without detection.",
		"Records are signed, and can be re-verified independently against the repository's committed roster of trusted signers.",
		"The population is complete for the window: every commit is classified, and commits warden cannot vouch for are listed as exceptions with reasons.",
	},
	Unsupported: []string{
		"That the configured checks are ADEQUATE. warden runs what the repository's .warden.yaml defines; whether that set is sufficient is a separate control.",
		"That a second person reviewed the change. warden records the gate, not the review — approval and separation of duties are evidenced by the forge's branch protection and review records.",
		"That what runs in production corresponds to these commits. Deployment is a downstream control with its own evidence.",
		"That the signing key was under the control of an authorized person. Key custody and offboarding are your access-control evidence, not warden's.",
		"Anything about commits before the adoption commit, when the control did not yet operate.",
	},
}

// WithApprovals returns a copy carrying the forge's review records, with the
// change-management control and the assertions restated to match.
//
// The control text is not "upgraded" — it is made specific. Approval evidence
// is only as good as the forge's records, and a self-approval or an admin merge
// that bypassed review shows up in the numbers rather than being smoothed over.
// A partial answer is the dangerous case: an auditor reading an upgraded CC8.1
// has no way to know the numbers under it cover only part of the population
// unless the control text says so, in the same paragraph, with the count.
func (e Evidence) WithApprovals(byCommit map[string]Approval) Evidence {
	e.ApprovalByCommit = byCommit

	total := len(byCommit)
	var undetermined int
	for _, a := range byCommit {
		if a.Undetermined {
			undetermined++
		}
	}
	incomplete := ""
	if undetermined > 0 {
		incomplete = fmt.Sprintf(" THIS APPROVAL EVIDENCE IS INCOMPLETE: the forge could not be read for %d of %d changes in the population. Those changes are reported as undetermined — warden makes no claim about them either way, and they must not be read as unapproved or as having bypassed a pull request.",
			undetermined, total)
	}

	controls := make([]Control, len(e.Controls))
	copy(controls, e.Controls)
	for i := range controls {
		if controls[i].ID != "CC8.1" && controls[i].ID != "A.8.32" {
			continue
		}
		controls[i].Evidences += " Approval is included: for each change, the pull request it arrived through, its author, and who approved it." + incomplete
		controls[i].Limits = "Approval is evidenced only as strongly as the forge records it. An approval by the author is reported as a self-approval and does not count as separation of duties; a change merged with administrator privileges past a required review appears as unapproved. Authorization — that the change was wanted — is still a separate control with separate evidence."
		if undetermined > 0 {
			controls[i].Limits += fmt.Sprintf(" It also does not evidence approval for %d of %d changes, whose forge record warden could not read; this control is unevidenced for those changes.",
				undetermined, total)
		}
	}
	e.Controls = controls

	supported := "For each change, the pull request it arrived through and the identities that approved it, as recorded by the forge."
	if undetermined > 0 {
		supported = fmt.Sprintf("For %d of %d changes, the pull request it arrived through and the identities that approved it, as recorded by the forge. The forge could not be read for the other %d, which carry no approval claim at all.",
			total-undetermined, total, undetermined)
	}
	e.Assertions.Supported = append(append([]string{}, e.Assertions.Supported...), supported)
	unsupported := make([]string, 0, len(e.Assertions.Unsupported))
	for _, u := range e.Assertions.Unsupported {
		if strings.Contains(u, "second person") {
			u = "That an approval was MEANINGFUL. The forge records who clicked approve, not whether they read the change; and an approval by the author is counted here as a self-approval, not as review."
		}
		unsupported = append(unsupported, u)
	}
	if undetermined > 0 {
		unsupported = append(unsupported, fmt.Sprintf(
			"Anything at all about the %d changes whose forge record could not be read. They are undetermined, not unapproved: the approval population in this report is %d, not %d.",
			undetermined, total-undetermined, total))
	}
	e.Assertions.Unsupported = unsupported
	return e
}

// ControlsFor returns the control catalogs named by key. Unknown keys are
// ignored by the domain; the delivery layer validates them so a typo is a
// usage error rather than a silently thinner report.
func ControlsFor(frameworks []string) []Control {
	if len(frameworks) == 0 {
		frameworks = KnownFrameworks()
	}
	var out []Control
	for _, f := range frameworks {
		out = append(out, controlCatalogue[strings.ToLower(f)]...)
	}
	return out
}

// KnownFrameworks lists the catalog keys, sorted for stable output.
func KnownFrameworks() []string {
	keys := make([]string, 0, len(controlCatalogue))
	for k := range controlCatalogue {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// controlCatalogue maps a framework key to the controls warden's data speaks
// to. It is deliberately short. Every control here is one where the gate's
// output is direct evidence; a longer list built on "well, adjacent to" is how
// an evidence package stops being read.
var controlCatalogue = map[string][]Control{
	"soc2": {
		{
			Framework: "SOC 2",
			ID:        "CC8.1",
			Name:      "Changes are authorized, designed, developed, configured, documented, tested, approved and implemented",
			Evidences: "The TESTED and DOCUMENTED elements: for each commit in the population, which checks ran, that they passed, against which tree, when, and signed by whom. Commits that reached the branch without that record are listed as exceptions.",
			Limits:    "Does not evidence AUTHORIZED or APPROVED. warden observes the gate, not the ticket or the reviewer; pair with the forge's branch-protection and pull-request review records.",
		},
		{
			Framework: "SOC 2",
			ID:        "CC7.1",
			Name:      "Detection of configuration changes and vulnerabilities",
			Evidences: "Where the repository configures a scanning step, that it ran on every gated change and that the change was refused when it failed. The step names in each record show which ran.",
			Limits:    "Evidences that the scan ran, not the scanner's coverage or its finding policy. A repository whose .warden.yaml has no scanning step will show none, which the report states rather than implies.",
		},
		{
			Framework: "SOC 2",
			ID:        "CC6.8",
			Name:      "Prevention and detection of unauthorized or malicious software",
			Evidences: "Detection of source changes that bypassed the gate. Signed, commit-bound records make an ungated commit visible as an exception rather than indistinguishable from a gated one.",
			Limits:    "Detective, not preventive, on its own — the gate runs on developer machines and can be bypassed with --no-verify. The preventive half is the forge-side required check that refuses to merge an unattested commit.",
		},
	},
	"iso27001": {
		{
			Framework: "ISO/IEC 27001:2022",
			ID:        "A.8.32",
			Name:      "Change management",
			Evidences: "That changes to source followed a defined procedure with recorded, signed verification, and that deviations from it are enumerable rather than invisible.",
			Limits:    "Covers source changes only. Infrastructure, configuration and data changes are outside anything warden observes.",
		},
		{
			Framework: "ISO/IEC 27001:2022",
			ID:        "A.8.29",
			Name:      "Security testing in development and acceptance",
			Evidences: "That the repository's configured security testing ran as a precondition of publication, per change, with the result recorded.",
			Limits:    "Says nothing about the depth or currency of those tests.",
		},
		{
			Framework: "ISO/IEC 27001:2022",
			ID:        "A.8.25",
			Name:      "Secure development life cycle",
			Evidences: "That a defined, enforced, machine-checked stage exists in the development life cycle, and evidence of its operation over the period.",
			Limits:    "One stage of the life cycle. Design review, threat modeling and dependency governance are evidenced elsewhere.",
		},
	},
}

// Approval is what the forge records about who signed off on a change.
//
// warden does not observe review; the forge does. Reading it here is what turns
// CC8.1 from half-evidenced into evidenced — but only as strongly as the forge's
// own records, which is why Independent is computed rather than assumed: an
// approval by the author is a record of a review that did not happen.
type Approval struct {
	// Found is false when no pull request could be associated with the commit,
	// which is not the same as "no approval" — a commit pushed straight to the
	// branch has no PR to have been approved.
	Found bool
	// Undetermined is true when warden could not get an answer from the forge
	// for this commit: an expired credential, a rate limit, a transient 5xx.
	//
	// It exists because the three other states are all POSITIVE CLAIMS, and a
	// forge that cannot answer must not be recorded as one that answered
	// "nothing". Undetermined outranks the rest: a record may carry a pull
	// request whose reviews could not be read, and warden may not report that
	// as "nobody approved it".
	Undetermined bool
	// Reason is the forge's own account of why, for the operator. It is
	// diagnostic only and is deliberately never rendered into the evidence
	// document: what an auditor needs is the count, not gh's error text.
	Reason string
	// PR is the pull request number the commit arrived through.
	PR int
	// Author is the login that opened it.
	Author string
	// Approvers are the logins that submitted an APPROVED review.
	Approvers []string
}

// Independent reports whether someone other than the author approved. Bots are
// counted only if a human did too — an automated approval is not a second pair
// of eyes, and a control that accepted one would be evidencing the opposite of
// what it claims.
func (a Approval) Independent() bool {
	for _, who := range a.Approvers {
		if who != a.Author && !isBot(who) {
			return true
		}
	}
	return false
}

func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]") || strings.HasSuffix(login, "-bot")
}

// ApprovalSummary counts separation of duties across the population.
type ApprovalSummary struct {
	// Collected is false when the report did not ask the forge, in which case
	// the numbers below mean nothing and must not be rendered.
	Collected int
	// Independent changes carried an approval from a human other than the author.
	Independent int
	// SelfApprovedOnly changes had approvals, all of them the author's.
	SelfApprovedOnly int
	// Unapproved changes came through a pull request nobody approved.
	Unapproved int
	// NoPullRequest changes were not associated with a pull request at all.
	NoPullRequest int
	// Undetermined changes are the ones the forge could not be read for. They
	// are not a finding — they are the absence of one — and must be rendered
	// separately from NoPullRequest, which is a finding.
	Undetermined int
}

// Determined is the number of changes the forge actually answered for. Every
// rate stated about approvals has to be over this, not over Collected, or a
// forge outage reads as a control failure.
func (s ApprovalSummary) Determined() int { return s.Collected - s.Undetermined }

// Approvals summarizes the approval records attached to the population.
func (e Evidence) Approvals() ApprovalSummary {
	var s ApprovalSummary
	for _, a := range e.ApprovalByCommit {
		s.Collected++
		switch {
		// First, because an unanswered lookup is not evidence of anything —
		// including of there being no pull request.
		case a.Undetermined:
			s.Undetermined++
		case !a.Found:
			s.NoPullRequest++
		case a.Independent():
			s.Independent++
		case len(a.Approvers) > 0:
			s.SelfApprovedOnly++
		default:
			s.Unapproved++
		}
	}
	return s
}
