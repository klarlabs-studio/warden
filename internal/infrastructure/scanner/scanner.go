// Package scanner reads a security scanner's machine-readable output so warden
// can reason about individual findings instead of a single exit code.
//
// Warden's security-scan step has always run a configured shell command and
// gated on its exit status. That is enough to say "the tree is dirty" and not
// enough to say "*your change* made it dirty", so an unrelated one-line commit
// inherits the repo's whole historical backlog as a precondition. Reading the
// report turns the gate from a boolean into a set, which is what delta gating
// (fail on what the diff introduced, warn on what it didn't) needs.
//
// The supported scanner is nox: `nox scan` writes a findings.json whose entries
// carry a stable Fingerprint, which is exactly the identity the delta and the
// committed baseline are both keyed on. Any other command stays on the old
// exit-code path — warden never guesses another tool's output contract.
package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReportFile is the name nox writes its JSON report under, inside the directory
// given to `-output`.
const ReportFile = "findings.json"

// BaselinePath is where nox keeps the committed roster of accepted findings,
// relative to the scanned tree.
const BaselinePath = ".nox/baseline.json"

// Finding is one scanner finding, reduced to the fields warden gates on.
type Finding struct {
	// Fingerprint is the scanner's stable identity for the finding. It is what
	// the baseline stores and what the delta compares, and it is also the thing
	// that silently changes when the scanner renumbers its rules — see
	// BaselineDrift.
	Fingerprint string
	RuleID      string
	// Severity is the scanner's own word ("critical", "high", "medium", …),
	// kept verbatim so a report never claims a lower severity than the scanner
	// did. Warden's own Severity scale tops out at "high".
	Severity string
	// Status is the scanner's disposition: empty or "open" for a live finding,
	// "baselined"/"suppressed"/"fixed" for one that has already been accepted.
	Status  string
	File    string
	Line    int
	Message string
}

// Location is where a finding sits in the tree, formatted for a human.
func (f Finding) Location() string {
	if f.File == "" {
		return ""
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// String renders a finding as one gate-output line.
func (f Finding) String() string {
	parts := []string{f.RuleID}
	if f.Severity != "" {
		parts[0] = fmt.Sprintf("%s (%s)", f.RuleID, f.Severity)
	}
	if loc := f.Location(); loc != "" {
		parts = append(parts, loc)
	}
	if f.Message != "" {
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, " ")
}

// Report is a parsed scanner run.
type Report struct {
	// ToolVersion is the scanner version that produced the report, as the
	// scanner itself recorded it.
	ToolVersion string
	Findings    []Finding
}

// noxReport mirrors the on-disk findings.json shape. It is deliberately a
// separate type from Finding: the wire schema is nox's to change, and warden's
// gate should not be written against Go-exported-looking JSON keys.
type noxReport struct {
	Meta struct {
		ToolVersion string `json:"tool_version"`
	} `json:"meta"`
	Findings []struct {
		RuleID      string `json:"RuleID"`
		Severity    string `json:"Severity"`
		Status      string `json:"Status"`
		Fingerprint string `json:"Fingerprint"`
		Message     string `json:"Message"`
		Location    struct {
			FilePath  string `json:"FilePath"`
			StartLine int    `json:"StartLine"`
		} `json:"Location"`
	} `json:"findings"`
}

// maxReportBytes caps how much report warden will read. A findings.json is
// kilobytes even for a badly neglected repo; anything past this is a bug or a
// hostile file, and the gate should refuse rather than exhaust memory inside a
// git hook.
const maxReportBytes = 64 << 20

// ReadReport parses the findings.json inside dir. A missing or malformed report
// is an error, not an empty result: callers fall back to exit-code gating on
// error, and silently treating "could not read" as "no findings" would turn a
// broken parse into a green gate.
func ReadReport(dir string) (Report, error) {
	data, err := readCapped(filepath.Join(dir, ReportFile), maxReportBytes)
	if err != nil {
		return Report{}, err
	}
	var raw noxReport
	if err := json.Unmarshal(data, &raw); err != nil {
		return Report{}, fmt.Errorf("parse %s: %w", ReportFile, err)
	}
	rep := Report{ToolVersion: raw.Meta.ToolVersion}
	for _, f := range raw.Findings {
		rep.Findings = append(rep.Findings, Finding{
			Fingerprint: f.Fingerprint,
			RuleID:      f.RuleID,
			Severity:    strings.ToLower(strings.TrimSpace(f.Severity)),
			Status:      strings.ToLower(strings.TrimSpace(f.Status)),
			File:        f.Location.FilePath,
			Line:        f.Location.StartLine,
			Message:     f.Message,
		})
	}
	return rep, nil
}

// Gating returns the findings that would fail the gate: those at or above the
// severity threshold that the scanner has not already suppressed. It mirrors
// what makes `nox scan` exit non-zero, because the report itself is unfiltered
// — findings.json carries every hit regardless of -severity-threshold, so a
// caller that gated on "the report is non-empty" would block on a medium in a
// repo configured to care only about high.
func (r Report) Gating(threshold string) []Finding {
	floor := severityRank(threshold)
	var out []Finding
	for _, f := range r.Findings {
		if suppressed(f.Status) {
			continue
		}
		if severityRank(f.Severity) < floor {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Fingerprints returns the set of fingerprints in the report, including
// suppressed findings.
//
// Suppressed ones are deliberately included: this set answers "did the scanner
// already see this at the base commit", and a finding that was baselined at the
// base commit was not introduced by the diff. Dropping a baseline entry is a
// deliberate act of surfacing existing debt, not of adding a vulnerability, and
// making that fail the author's unrelated push is the exact wall delta gating
// exists to remove.
func (r Report) Fingerprints() map[string]bool {
	set := make(map[string]bool, len(r.Findings))
	for _, f := range r.Findings {
		if f.Fingerprint != "" {
			set[f.Fingerprint] = true
		}
	}
	return set
}

// SplitIntroduced partitions gating findings into the ones absent from base
// (introduced by the change under review) and the ones the tree already
// carried. A finding with no fingerprint cannot be matched against the base and
// counts as introduced — fail closed, since the alternative is a scanner quirk
// silently waving new findings through.
func SplitIntroduced(gating []Finding, base map[string]bool) (introduced, preexisting []Finding) {
	for _, f := range gating {
		if f.Fingerprint != "" && base[f.Fingerprint] {
			preexisting = append(preexisting, f)
			continue
		}
		introduced = append(introduced, f)
	}
	return introduced, preexisting
}

// Baseline is the committed roster of accepted findings.
type Baseline struct {
	Fingerprints []string
}

// ReadBaseline loads the baseline committed under root, if there is one. A
// missing baseline is not an error: most repos start without one.
func ReadBaseline(root string) (Baseline, bool, error) {
	data, err := readCapped(filepath.Join(root, BaselinePath), maxReportBytes)
	if os.IsNotExist(err) {
		return Baseline{}, false, nil
	}
	if err != nil {
		return Baseline{}, false, err
	}
	var raw struct {
		Entries []struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Baseline{}, false, fmt.Errorf("parse %s: %w", BaselinePath, err)
	}
	var b Baseline
	for _, e := range raw.Entries {
		if e.Fingerprint != "" {
			b.Fingerprints = append(b.Fingerprints, e.Fingerprint)
		}
	}
	return b, true, nil
}

// BaselineDrift reports whether a non-empty baseline matched nothing at all in
// the current report, and how many of its entries did match.
//
// This is the signature of scanner version drift. Renumbering rules between
// releases changes a finding's fingerprint, so every baseline entry stops
// matching at once and the entire accepted corpus reads as net-new: one repo
// saw 729 baseline entries match nothing and CI report 240 phantom criticals,
// failing every release for a month. A partial match is normal (code moved, a
// finding was fixed); a total miss against a baseline of any size is not
// evidence of hundreds of new vulnerabilities, it is evidence the two sides are
// no longer speaking the same fingerprint dialect. Saying so is the difference
// between a five-minute fix and a month of red.
func BaselineDrift(b Baseline, rep Report) (drifted bool, matched int) {
	if len(b.Fingerprints) == 0 {
		return false, 0
	}
	current := rep.Fingerprints()
	for _, fp := range b.Fingerprints {
		if current[fp] {
			matched++
		}
	}
	// A clean tree legitimately reports nothing at all, and then no baseline
	// entry can match. That is a fixed repo, not drift.
	if len(rep.Findings) == 0 {
		return false, 0
	}
	return matched == 0, matched
}

// suppressedStatuses are the dispositions that mean "the scanner already
// accounted for this finding". Anything else — including a status warden does
// not recognize — gates, so a new scanner disposition cannot quietly disable
// the gate.
var suppressedStatuses = map[string]bool{
	"baselined":    true,
	"suppressed":   true,
	"waived":       true,
	"fixed":        true,
	"notaffected":  true,
	"not_affected": true,
}

func suppressed(status string) bool { return suppressedStatuses[status] }

// severityRank orders the scanner's severity words. An unrecognized severity
// ranks as high: warden must not drop a finding just because the scanner
// invented a new word for "bad".
func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return 0
	case "info", "informational", "note":
		return 1
	case "low":
		return 2
	case "medium", "moderate":
		return 3
	case "critical":
		return 5
	default: // "high" and anything unknown
		return 4
	}
}

// readCapped reads a file, refusing anything larger than max.
func readCapped(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, info.Size(), limit)
	}
	return os.ReadFile(path) //nolint:gosec // path is composed from warden-controlled dirs
}
