package feedback

import (
	"bufio"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/domain"
)

// ConfigAnchorRule is the rule slot of a problems line that anchors a
// location-less failure to the configuration line declaring the step's
// command. The anchor is where the step is configured, not where warden saw a
// problem, and the rule says so on every such line.
const ConfigAnchorRule = "config-anchor"

// ConfigAnchor is the configuration line that declares a step's command:
// File relative to the repository root, Line 1-based.
type ConfigAnchor struct {
	File string
	Line int
}

// RenderProblems writes the document in the line format editors' problem
// matchers read:
//
//	warden: feedback <state>, <freshness> (<mode>, branch <branch>): <counts>
//	<file>:<line>: <error|warning|info>: [<step>/<rule>] <message>
//	warden: <step> failed: <summary>
//	<file>:<line>: error: [<step>/config-anchor] <step> failed: <summary>
//	warden: <step> could not run (<blocker>): <summary>
//
// The first line is always printed and states freshness. A file:line line is
// printed only for a finding the document locates. A failing step with no
// located finding prints a warden: line, and, when anchors names where its
// command is configured, a diagnostic at that line marked config-anchor so an
// editor can show it. A step the machine prevented from running is reported
// as such, never as a failure of the change. While the document does not
// describe the worktree as it is now, only the warden: lines are printed.
//
// Messages are cut to their first line: the full output is in the JSON.
func RenderProblems(w io.Writer, doc domain.FeedbackDocument, anchors map[domain.StepName]ConfigAnchor) error {
	bw := bufio.NewWriter(w)
	branch := doc.Snapshot.Branch
	if branch == "" {
		branch = "(unborn)"
	}
	_, _ = fmt.Fprintf(bw, "warden: feedback %s, %s (%s, branch %s): %s\n",
		doc.State, freshnessLabel(doc), doc.Mode, oneLine(branch), statusCounts(doc.Steps))
	show, why := Diagnosable(doc)
	if why != "" {
		_, _ = fmt.Fprintf(bw, "warden: %s: %s\n", freshnessLabel(doc), why)
	}
	if msg := oneLine(doc.Message); msg != "" {
		_, _ = fmt.Fprintf(bw, "warden: watcher: %s\n", msg)
	}
	if show {
		for i := range doc.Steps {
			writeStepProblems(bw, &doc.Steps[i], anchors)
		}
	}
	return bw.Flush()
}

func writeStepProblems(w io.Writer, step *domain.FeedbackStep, anchors map[domain.StepName]ConfigAnchor) {
	if step.Status != domain.FeedbackStepFail {
		return
	}
	name := oneLine(string(step.Step))
	summary := oneLine(step.Summary)
	if summary == "" {
		summary = "no summary"
	}
	if blocked(step) {
		_, _ = fmt.Fprintf(w, "warden: %s could not run (%s): %s\n", name, oneLine(string(step.Blocker)), summary)
		return
	}
	printed := false
	for _, f := range step.Findings {
		if !located(f) {
			continue
		}
		printed = true
		tag := name
		if rule := oneLine(f.Rule); rule != "" {
			tag += "/" + rule
		}
		message := oneLine(f.Message)
		if message == "" {
			message = summary
		}
		_, _ = fmt.Fprintf(w, "%s:%d: %s: [%s] %s\n", oneLine(f.File), f.Line, problemSeverity(f.Severity), tag, message)
	}
	if printed {
		return
	}
	_, _ = fmt.Fprintf(w, "warden: %s failed: %s\n", name, summary)
	if a, ok := anchors[step.Step]; ok && a.File != "" && a.Line > 0 {
		_, _ = fmt.Fprintf(w, "%s:%d: error: [%s/%s] %s failed: %s\n", oneLine(a.File), a.Line, name, ConfigAnchorRule, name, summary)
	}
}

// problemSeverity maps warden's severities onto the three words problem
// matchers understand.
func problemSeverity(s domain.Severity) string {
	switch s {
	case domain.SeverityHigh:
		return "error"
	case domain.SeverityMedium:
		return "warning"
	default:
		return "info"
	}
}

// freshnessLabel is the document's freshness, or a marker when the reader
// has not judged it.
func freshnessLabel(doc domain.FeedbackDocument) string {
	if doc.Freshness == "" {
		return "unjudged"
	}
	return string(doc.Freshness)
}
