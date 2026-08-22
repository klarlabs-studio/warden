package domain

import (
	"strings"
	"testing"
	"time"
)

func at(day int) string {
	return time.Date(2026, 2, day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func gated(sha string, day int) CommitStatus {
	return CommitStatus{
		SHA: sha, Date: at(day), Author: "dev", Subject: "s",
		HasNote: true, ChainIntact: true, Steps: []StepName{"test", "lint"},
	}
}

func report(commits ...CommitStatus) AuditReport {
	return AuditReport{Branch: "main", Adoption: "aaaa000000000000", Commits: commits}
}

func opts(from, to time.Time) EvidenceOptions {
	return EvidenceOptions{
		Tool: "warden test", Repository: "example/repo",
		From: from, To: to, Now: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

func day(d int) time.Time { return time.Date(2026, 2, d, 0, 0, 0, 0, time.UTC) }

// An engagement covers a window. Evidence from outside it invites questions
// the engagement is not asking.
func TestEvidence_KeepsOnlyThePeriod(t *testing.T) {
	e := NewEvidence(report(
		gated("a", 1), gated("b", 10), gated("c", 20),
	), opts(day(5), day(15)))

	if len(e.Population) != 1 || e.Population[0].SHA != "b" {
		t.Fatalf("population = %+v, want only the in-window commit", e.Population)
	}
}

// No bounds means the whole history — the same population `warden audit`
// shows. Two commands that disagree about what happened are worse than one.
func TestEvidence_NoPeriodMeansEverything(t *testing.T) {
	e := NewEvidence(report(gated("a", 1), gated("b", 20)), opts(time.Time{}, time.Time{}))
	if len(e.Population) != 2 {
		t.Fatalf("population = %d, want all commits", len(e.Population))
	}
}

// A commit whose timestamp will not parse must still be counted. Dropping it
// silently shrinks the population, and a population that cannot be reconciled
// against `git log` is the one thing this must never produce.
func TestEvidence_AnUnparseableDateIsStillInThePopulation(t *testing.T) {
	odd := gated("weird", 10)
	odd.Date = "not a date"

	e := NewEvidence(report(odd), opts(day(5), day(15)))

	if len(e.Population) != 1 {
		t.Fatalf("dropped a commit for having an unreadable date: %+v", e.Population)
	}
}

// Every commit warden could not vouch for has to appear, with a reason
// specific enough to act on.
func TestEvidence_ExceptionsCarryTheirSpecificReason(t *testing.T) {
	bypass := CommitStatus{SHA: "b1", Date: at(10), Subject: "s"}
	unpushed := CommitStatus{SHA: "b2", Date: at(10), Subject: "s", NoRemoteRef: true}
	old := CommitStatus{SHA: "b3", Date: at(10), Subject: "s", PreSpanProvenance: true}
	squashed := CommitStatus{SHA: "b4", Date: at(10), Subject: "s", ReattestableFrom: "cafe00000000"}
	defective := CommitStatus{SHA: "b5", Date: at(10), Subject: "s", HasNote: true, NoteDefect: "UNBOUND"}

	e := NewEvidence(report(gated("ok", 10), bypass, unpushed, old, squashed, defective), opts(day(1), day(28)))
	ex := e.Exceptions()

	if len(ex) != 5 {
		t.Fatalf("exceptions = %d, want the five non-vouched commits", len(ex))
	}
	byName := map[string]Exception{}
	for _, x := range ex {
		byName[x.SHA] = x
	}
	for sha, want := range map[string]string{
		"b1": "no warden note",
		"b2": "never pushed",
		"b3": "unattributable",
		"b4": "content-identical",
		"b5": "UNBOUND",
	} {
		if got := byName[sha].Reason; !strings.Contains(got, want) {
			t.Errorf("%s reason = %q, want it to mention %q", sha, got, want)
		}
	}
	// A squash-merge gap is remediable, and saying so is the difference
	// between a finding and a to-do.
	if !strings.Contains(byName["b4"].Remediation, "reattest") {
		t.Errorf("no remediation offered for a reattestable commit: %q", byName["b4"].Remediation)
	}
}

// A commit published inside a gated push span is not an exception: the span is
// what vouches for it.
func TestEvidence_CoveredCommitsAreNotExceptions(t *testing.T) {
	covered := CommitStatus{SHA: "c1", Date: at(10), Subject: "s", CoveredBy: "tip000000000"}

	e := NewEvidence(report(gated("tip", 10), covered), opts(day(1), day(28)))

	if got := len(e.Exceptions()); got != 0 {
		t.Fatalf("exceptions = %d, want none: %+v", got, e.Exceptions())
	}
}

// The digest exists so an edited report can be told from a re-run one. It must
// therefore depend on the verdicts and not on when the report was made.
func TestEvidence_DigestIsStableAcrossRunsAndMovesWithTheVerdicts(t *testing.T) {
	first := NewEvidence(report(gated("a", 10), gated("b", 11)), opts(day(1), day(28)))

	later := opts(day(1), day(28))
	later.Now = later.Now.Add(72 * time.Hour)
	second := NewEvidence(report(gated("a", 10), gated("b", 11)), later)

	if first.Digest() != second.Digest() {
		t.Error("digest changed on a re-run of the same window")
	}

	// Order must not matter either: the same facts, listed differently, are
	// the same evidence.
	reordered := NewEvidence(report(gated("b", 11), gated("a", 10)), opts(day(1), day(28)))
	if reordered.Digest() != first.Digest() {
		t.Error("digest depends on commit ordering")
	}

	broken := gated("b", 11)
	broken.ChainIntact = false
	changed := NewEvidence(report(gated("a", 10), broken), opts(day(1), day(28)))
	if changed.Digest() == first.Digest() {
		t.Error("digest did not move when a verdict did")
	}
}

// The disclaimers are the load-bearing part. Evidence is rejected for claiming
// too much far more often than for proving too little, so every control names
// its limits and the package names what it cannot speak to at all.
func TestEvidence_EveryControlStatesItsLimits(t *testing.T) {
	e := NewEvidence(report(gated("a", 10)), opts(time.Time{}, time.Time{}))

	if len(e.Controls) == 0 {
		t.Fatal("no controls mapped")
	}
	for _, c := range e.Controls {
		if strings.TrimSpace(c.Limits) == "" {
			t.Errorf("%s %s claims no limits", c.Framework, c.ID)
		}
		if strings.TrimSpace(c.Evidences) == "" {
			t.Errorf("%s %s evidences nothing", c.Framework, c.ID)
		}
	}

	// The four things a source gate structurally cannot show.
	for _, want := range []string{"ADEQUATE", "second person", "production", "key"} {
		found := false
		for _, u := range e.Assertions.Unsupported {
			if strings.Contains(u, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("unsupported claims do not mention %q", want)
		}
	}
}

// Approval is the half of CC8.1 warden does not observe, and the report has to
// say so on the control itself — not only in a general disclaimer somebody
// skips.
func TestEvidence_ChangeManagementControlDisclaimsApproval(t *testing.T) {
	e := NewEvidence(report(gated("a", 10)), EvidenceOptions{Frameworks: []string{"soc2"}})

	for _, c := range e.Controls {
		if c.ID != "CC8.1" {
			continue
		}
		if !strings.Contains(c.Limits, "APPROVED") {
			t.Errorf("CC8.1 limits do not disclaim approval: %q", c.Limits)
		}
		return
	}
	t.Fatal("CC8.1 not in the SOC 2 catalog")
}

func TestEvidence_FrameworkSelection(t *testing.T) {
	only := NewEvidence(report(gated("a", 10)), EvidenceOptions{Frameworks: []string{"iso27001"}})
	for _, c := range only.Controls {
		if !strings.HasPrefix(c.Framework, "ISO") {
			t.Errorf("asked for iso27001, got %s %s", c.Framework, c.ID)
		}
	}
	if all := NewEvidence(report(gated("a", 10)), EvidenceOptions{}); len(all.Controls) <= len(only.Controls) {
		t.Error("no framework selected should mean every catalog")
	}
}

// An auditor should be able to re-derive the verdict themselves rather than
// trust the report asserting it.
func TestEvidence_OffersTheVerificationCommand(t *testing.T) {
	e := NewEvidence(report(gated("a", 10)), opts(time.Time{}, time.Time{}))
	if !strings.Contains(e.VerifyCommand(), "warden verify --range") {
		t.Errorf("VerifyCommand = %q", e.VerifyCommand())
	}
	if !strings.Contains(e.VerifyCommand(), "aaaa00000000") {
		t.Errorf("VerifyCommand does not start at adoption: %q", e.VerifyCommand())
	}
}
