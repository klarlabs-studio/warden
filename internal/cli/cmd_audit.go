package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// cmdAudit handles `warden audit`: a compliance-export report of provenance for
// every commit since adoption (§9). Unlike doctor — which gates CI by exiting
// non-zero on drift — audit is purely informational and always exits 0 on a
// successful report, so it can be piped into a compliance doc or PR without a
// failing status masking the output.
func cmdAudit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	branchFlag := fs.String("branch", "", "branch to audit (default: current)")
	formatFlag := fs.String("format", "text", "output format: text | json | md")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "audit", "branch") {
		return 2
	}
	if *formatFlag != "text" && *formatFlag != "json" && *formatFlag != "md" {
		_, _ = fmt.Fprintf(stderr, "warden: unknown --format %q (want text, json, or md)\n", *formatFlag)
		return 2
	}

	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	report, err := svc.Audit(*branchFlag)
	if err != nil {
		return fail(stderr, err)
	}

	switch *formatFlag {
	case "json":
		return printAuditJSON(stdout, stderr, report)
	case "md":
		printAuditMarkdown(stdout, report)
	default:
		printAuditText(stdout, report)
	}
	return 0
}

// auditExport is the stable JSON shape for the compliance export. It is a
// delivery-layer projection of AuditReport with snake_case field names an
// auditor's tooling can consume, decoupled from the domain struct.
type auditExport struct {
	Branch      string        `json:"branch"`
	Adoption    string        `json:"adoption"`
	GeneratedAt string        `json:"generated_at"`
	Summary     auditSummary  `json:"summary"`
	Commits     []auditCommit `json:"commits"`
}

type auditSummary struct {
	Verified int `json:"verified"`
	// Defective counts commits whose note does NOT attest them. Renamed from
	// "intact": the value now counts the failures, not the successes, and a field
	// that kept the old name would invert its own meaning for every consumer.
	Defective  int `json:"defective"`
	Unverified int `json:"unverified"`
	// Covered and Unknown were previously summed into Unverified, which reported
	// a fully-gated multi-commit push as unchecked. They are exported separately
	// so a consumer can tell "warden vouches for this via the push span" and
	// "warden was never in a position to say" apart from a real gap.
	Covered int `json:"covered"`
	Unknown int `json:"unknown"`
}

type auditCommit struct {
	SHA         string            `json:"sha"`
	Author      string            `json:"author"`
	Date        string            `json:"date"`
	Subject     string            `json:"subject"`
	Validated   bool              `json:"validated"`
	ChainIntact bool              `json:"chain_intact"`
	RunID       string            `json:"run_id"`
	Steps       []domain.StepName `json:"steps"`
	// ReattestableFrom names a validated commit reproducing this commit's tree,
	// when this commit has no note of its own. A compliance reader needs the
	// distinction: content that was gated under a pre-squash id is a binding gap
	// a maintainer can close, not an unchecked change. Omitted when absent.
	ReattestableFrom string `json:"reattestable_from,omitempty"`
}

// printAuditJSON marshals the report into the export shape. generated_at is a
// wall-clock stamp (there is no injected clock); tests tolerate its value.
func printAuditJSON(stdout, stderr io.Writer, r domain.AuditReport) int {
	t := r.Counts()
	export := auditExport{
		Branch:      r.Branch,
		Adoption:    r.Adoption,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Summary: auditSummary{
			Verified: t.Verified, Defective: t.Defective, Unverified: t.Unverified,
			Covered: t.Covered, Unknown: t.Unknown,
		},
		Commits: make([]auditCommit, 0, len(r.Commits)),
	}
	for i := range r.Commits {
		c := &r.Commits[i]
		export.Commits = append(export.Commits, auditCommit{
			SHA:     c.SHA,
			Author:  c.Author,
			Date:    c.Date,
			Subject: c.Subject,
			// Validated means the note ATTESTS this commit, not merely that one
			// exists. It was c.HasNote, so a commit whose note failed to attest it
			// exported validated=true — in the compliance artifact, of all places.
			Validated:        c.ChainIntact,
			ChainIntact:      c.ChainIntact,
			RunID:            c.RunID,
			Steps:            c.Steps,
			ReattestableFrom: c.ReattestableFrom,
		})
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(export); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// printAuditText renders a human-readable audit: a framed header, one line per
// commit, and a summary — the doctor view reframed as an evidence report.
func printAuditText(w io.Writer, r domain.AuditReport) {
	_, _ = fmt.Fprintln(w, "warden audit — commit provenance report")
	_, _ = fmt.Fprintf(w, "  branch:    %s\n", r.Branch)
	_, _ = fmt.Fprintf(w, "  adoption:  %s\n", short(r.Adoption))
	_, _ = fmt.Fprintf(w, "  generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	_, _ = fmt.Fprintln(w, "commits:")
	for i := range r.Commits {
		c := &r.Commits[i]
		switch {
		case c.HasNote:
			glyph, state := "✓", "chain-intact"
			if !c.ChainIntact {
				glyph, state = "⚠", noteDefectLabel(c.NoteDefect)
			}
			_, _ = fmt.Fprintf(w, "  %s %s  %s  %s  (%s, %d steps, %s)\n",
				glyph, short(c.SHA), c.Date, truncate(c.Subject, 40), c.RunID, len(c.Steps), state)
		case c.Reattestable():
			_, _ = fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (reattestable from %s)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40), short(c.ReattestableFrom))
		default:
			_, _ = fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (no warden note)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		}
	}
	_, _ = fmt.Fprint(w, summaryLine("summary: ", r.Counts()))
}

// printAuditMarkdown renders a table plus a summary line, suitable for pasting
// into a compliance doc or PR body.
func printAuditMarkdown(w io.Writer, r domain.AuditReport) {
	_, _ = fmt.Fprintf(w, "# Warden audit — `%s` since adoption `%s`\n\n", r.Branch, short(r.Adoption))
	_, _ = fmt.Fprintln(w, "| SHA | Date | Subject | Status | Run |")
	_, _ = fmt.Fprintln(w, "| --- | --- | --- | --- | --- |")
	for i := range r.Commits {
		c := &r.Commits[i]
		status, run := "unverified", "—"
		switch {
		case c.HasNote && c.ChainIntact:
			status, run = "verified (chain-intact)", c.RunID
		case c.HasNote:
			// "verified (TAMPERED)" was doubly wrong: it called an unattested commit
			// verified, and named the innocent cases tampering.
			status, run = "unverified ("+c.NoteDefect+")", c.RunID
		case c.Reattestable():
			status, run = "unverified (reattestable)", "`"+short(c.ReattestableFrom)+"`"
		}
		_, _ = fmt.Fprintf(w, "| `%s` | %s | %s | %s | %s |\n",
			short(c.SHA), c.Date, truncate(c.Subject, 40), status, run)
	}
	_, _ = fmt.Fprint(w, "\n**Summary:** "+summaryLine("", r.Counts()))
}
