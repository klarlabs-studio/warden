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
	if *formatFlag != "text" && *formatFlag != "json" && *formatFlag != "md" {
		fmt.Fprintf(stderr, "warden: unknown --format %q (want text, json, or md)\n", *formatFlag)
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
	Verified   int `json:"verified"`
	Intact     int `json:"intact"`
	Unverified int `json:"unverified"`
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
}

// printAuditJSON marshals the report into the export shape. generated_at is a
// wall-clock stamp (there is no injected clock); tests tolerate its value.
func printAuditJSON(stdout, stderr io.Writer, r domain.AuditReport) int {
	verified, intact, unverified := r.Counts()
	export := auditExport{
		Branch:      r.Branch,
		Adoption:    r.Adoption,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Summary:     auditSummary{Verified: verified, Intact: intact, Unverified: unverified},
		Commits:     make([]auditCommit, 0, len(r.Commits)),
	}
	for _, c := range r.Commits {
		export.Commits = append(export.Commits, auditCommit{
			SHA:         c.SHA,
			Author:      c.Author,
			Date:        c.Date,
			Subject:     c.Subject,
			Validated:   c.HasNote,
			ChainIntact: c.ChainIntact,
			RunID:       c.RunID,
			Steps:       c.Steps,
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
	fmt.Fprintln(w, "warden audit — commit provenance report")
	fmt.Fprintf(w, "  branch:    %s\n", r.Branch)
	fmt.Fprintf(w, "  adoption:  %s\n", short(r.Adoption))
	fmt.Fprintf(w, "  generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintln(w, "commits:")
	for _, c := range r.Commits {
		if c.HasNote {
			state := "chain-intact"
			if !c.ChainIntact {
				state = "TAMPERED"
			}
			fmt.Fprintf(w, "  ✓ %s  %s  %s  (%s, %d steps, %s)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40), c.RunID, len(c.Steps), state)
		} else {
			fmt.Fprintf(w, "  ✗ %s  %s  %s  UNVERIFIED (no warden note)\n",
				short(c.SHA), c.Date, truncate(c.Subject, 40))
		}
	}
	verified, intact, unverified := r.Counts()
	fmt.Fprintf(w, "summary: %d verified (%d chain-intact), %d unverified since adoption\n",
		verified, intact, unverified)
}

// printAuditMarkdown renders a table plus a summary line, suitable for pasting
// into a compliance doc or PR body.
func printAuditMarkdown(w io.Writer, r domain.AuditReport) {
	fmt.Fprintf(w, "# Warden audit — `%s` since adoption `%s`\n\n", r.Branch, short(r.Adoption))
	fmt.Fprintln(w, "| SHA | Date | Subject | Status | Run |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- |")
	for _, c := range r.Commits {
		status, run := "unverified", "—"
		switch {
		case c.HasNote && c.ChainIntact:
			status, run = "verified (chain-intact)", c.RunID
		case c.HasNote:
			status, run = "verified (TAMPERED)", c.RunID
		}
		fmt.Fprintf(w, "| `%s` | %s | %s | %s | %s |\n",
			short(c.SHA), c.Date, truncate(c.Subject, 40), status, run)
	}
	verified, intact, unverified := r.Counts()
	fmt.Fprintf(w, "\n**Summary:** %d verified (%d chain-intact), %d unverified since adoption.\n",
		verified, intact, unverified)
}
