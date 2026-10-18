package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cmdWhy handles `warden why [commit]`, explaining what warden did for a past
// commit by reading its provenance note: which rules matched, which steps ran,
// the agent per step, and the signed evidence root. It answers "why did (or
// didn't) the gate do X here" from the record itself — no re-run.
func cmdWhy(args []string, stdout, stderr io.Writer) int {
	commit := "HEAD"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		commit = args[0]
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	res, err := svc.Verify(commit)
	if err != nil {
		return fail(stderr, err)
	}
	if res.Record == nil {
		_, _ = fmt.Fprintf(stdout, "no warden note on %s — the commit was made outside the gate or predates adoption.\n", short(res.SHA))
		return 1
	}

	rec := res.Record
	_, _ = fmt.Fprintf(stdout, "commit:        %s\n", short(res.SHA))
	_, _ = fmt.Fprintf(stdout, "run:           %s\n", rec.RunID)
	if rec.Timestamp != "" {
		_, _ = fmt.Fprintf(stdout, "when:          %s\n", rec.Timestamp)
	}
	if rec.WardenVersion != "" {
		_, _ = fmt.Fprintf(stdout, "warden:        %s\n", rec.WardenVersion)
	}

	_, _ = fmt.Fprintf(stdout, "steps run:    ")
	for _, s := range rec.StepsRun {
		_, _ = fmt.Fprintf(stdout, " %s", s)
		if rec.Agent != nil {
			if a := rec.Agent[s]; a != "" {
				_, _ = fmt.Fprintf(stdout, "(agent=%s)", a)
			}
		}
	}
	_, _ = fmt.Fprintln(stdout)
	// The note lists the steps the policy gated, not whether each one did its
	// work: a step served from the input cache, or an agent step skipped because
	// no agent was configured, is recorded exactly like one that executed. Say
	// so rather than let "steps run" vouch for a review nobody performed. The
	// live run output (and axi/MCP results) name skipped and cached steps.
	//
	// A profiled run's note does record them, signed, and then they are shown
	// instead of the caveat.
	switch {
	case rec.StepOutcomes != nil:
		_, _ = fmt.Fprintf(stdout, "outcomes:      %s\n", whyOutcomes(rec))
	case len(rec.StepsRun) > 0:
		_, _ = fmt.Fprintln(stdout, whyOutcomesCaveat)
	}
	if rec.Profile != nil {
		_, _ = fmt.Fprintf(stdout, "profile:       %s\n", whyProfile(*rec.Profile))
	}
	if rec.Approval != nil {
		_, _ = fmt.Fprintf(stdout, "approval:      %s\n", whyApproval(*rec.Approval))
	}
	if rec.Profile != nil {
		_, _ = fmt.Fprintf(stdout, "authorship:    %s\n", whyAuthorship(rec.Authorship))
	}

	if len(rec.MatchedRules) > 0 {
		_, _ = fmt.Fprintf(stdout, "matched rules: %s\n", strings.Join(rec.MatchedRules, ", "))
	} else {
		_, _ = fmt.Fprintln(stdout, "matched rules: (none — base policy)")
	}

	_, _ = fmt.Fprintf(stdout, "evidence:      %d records, chain %s\n", len(rec.Evidence), chainState(res.Validated))
	_, _ = fmt.Fprintf(stdout, "signature:     %s\n", whySignature(res))
	// The trace is the agent's own claim; what warden adds is that this exact
	// record existed when the commit was gated. Report whether it STILL matches,
	// because a digest that no longer matches is the finding.
	if t := rec.AgentTrace; t != nil {
		state := "notarized"
		raw, err := os.ReadFile(filepath.Join(svc.Repo().Dir, t.Path))
		switch {
		case err != nil:
			state = "notarized, but the record is no longer present"
		case !t.Matches(raw):
			state = "CHANGED SINCE — the record no longer matches what was signed"
		}
		_, _ = fmt.Fprintf(stdout, "agent trace:   %s (%s, spec %s)\n", t.Path, state, t.SpecVersion)
	}
	if len(rec.Dependencies) > 0 {
		_, _ = fmt.Fprintf(stdout, "sbom:          %d lockfile(s)\n", len(rec.Dependencies))
		for _, m := range rec.Dependencies {
			_, _ = fmt.Fprintf(stdout, "  %-9s %s  %s\n", m.Ecosystem, m.Path, m.Digest)
		}
	}
	return 0
}

// whyOutcomesCaveat qualifies the step list read from a note, which does not
// record whether a step executed, was cached, or was skipped.
const whyOutcomesCaveat = "outcomes:      not recorded in the note — a cached step, or an agent step skipped for want of an agent, is listed like one that executed"

// whyOutcomes renders the signed per-step observations in the order the steps
// were planned. A planned step with no observation is said to be unobserved
// rather than left out, so the list cannot read as complete when it is not.
func whyOutcomes(rec *domain.RunRecord) string {
	var parts []string
	seen := map[domain.StepName]bool{}
	render := func(s domain.StepName) {
		seen[s] = true
		obs, ok := rec.StepOutcomes[s]
		if !ok {
			parts = append(parts, string(s)+" not observed")
			return
		}
		part := string(s) + " " + string(obs.Outcome)
		if detail := whyObservationDetail(obs); detail != "" {
			part += " (" + detail + ")"
		}
		parts = append(parts, part)
	}
	for _, s := range rec.StepsRun {
		render(s)
	}
	extra := make([]domain.StepName, 0, len(rec.StepOutcomes))
	for s := range rec.StepOutcomes {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	slices.Sort(extra)
	for _, s := range extra {
		render(s)
	}
	return strings.Join(parts, ", ")
}

// whyObservationDetail names the agent and each agent boundary observation
// the note records. An unobserved boundary is not mentioned, and an observed
// false says so: "tree changed" is a finding, not a missing field.
func whyObservationDetail(obs domain.StepObservation) string {
	var parts []string
	if obs.Agent != "" {
		parts = append(parts, "agent="+obs.Agent)
	}
	if obs.Isolated != nil && *obs.Isolated {
		parts = append(parts, "isolated")
	}
	if obs.TreeUnchanged != nil {
		if *obs.TreeUnchanged {
			parts = append(parts, "tree unchanged")
		} else {
			parts = append(parts, "tree changed")
		}
	}
	if obs.EnvAllowlisted != nil && *obs.EnvAllowlisted {
		parts = append(parts, "env allowlisted")
	}
	return strings.Join(parts, ", ")
}

// whyProfile renders the recorded profile and whether its digest matches this
// warden's own definition of that version. The name alone is a claim; the
// digest comparison is what a reader can actually check.
func whyProfile(ref domain.ProfileRef) string {
	p, err := domain.ParseComplianceProfile(ref.String())
	switch {
	case err != nil:
		return ref.String() + " (unknown to this warden)"
	case p.Digest() != ref.Digest:
		return ref.String() + " (definition DIFFERS from this warden's)"
	default:
		return ref.String() + " (definition matches this warden's)"
	}
}

// whyApproval renders the approval gate's record. A tty or tui channel means an
// interactive surface answered; it is not proof a person did.
func whyApproval(a domain.ApprovalRef) string {
	if !a.Required {
		return "not required"
	}
	s := "required, answered via " + string(a.Channel)
	if a.Principal != "" {
		s += " (" + a.Principal + ")"
	}
	return s
}

// whyAuthorship renders the signed agent declarations of a profiled note. No
// declaration is stated as exactly that, never as human authorship.
func whyAuthorship(a *domain.AuthorshipSignals) string {
	if !a.Declared() {
		return "no agent declaration observed (this does not say a person wrote it)"
	}
	var parts []string
	for _, t := range a.Trailers {
		parts = append(parts, shortSHA(t.Commit)+" carries "+strconv.Quote(t.Trailer))
	}
	for _, m := range a.EnvMarkers {
		parts = append(parts, "gated under "+m.Name+" ("+m.Agent+")")
	}
	return "agent declared: " + strings.Join(parts, "; ")
}

func chainState(intact bool) string {
	if intact {
		return "intact"
	}
	return "BROKEN"
}

func whySignature(res service.VerifyResult) string {
	switch {
	case res.SignatureValid:
		return "valid, signed by " + res.Signer
	case res.Signed:
		return "present but INVALID"
	default:
		return "unsigned"
	}
}
