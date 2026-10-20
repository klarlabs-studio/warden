package feedback

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// SARIF 2.1.0 identifiers.
const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"
	// sarifSourceRoot is the uriBaseId a result's relative file is resolved
	// against; originalUriBaseIds maps it to the worktree.
	sarifSourceRoot = "SRCROOT"
	wardenURI       = "https://github.com/klarlabs-studio/warden"
)

// Notification descriptor ids, stable so a consumer can match on them.
const (
	sarifNoteFreshness = "warden/freshness"
	sarifNoteBlocked   = "warden/blocked"
	sarifNoteState     = "warden/state"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool               sarifTool                   `json:"tool"`
	Invocations        []sarifInvocation           `json:"invocations"`
	OriginalURIBaseIDs map[string]sarifArtifactLoc `json:"originalUriBaseIds,omitempty"`
	Results            []sarifResult               `json:"results"`
	Properties         sarifRunProperties          `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID string `json:"id"`
}

type sarifInvocation struct {
	ExecutionSuccessful bool                `json:"executionSuccessful"`
	EndTimeUTC          string              `json:"endTimeUtc,omitempty"`
	Notifications       []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Descriptor sarifDescriptorRef `json:"descriptor"`
	Level      string             `json:"level"`
	Message    sarifMessage       `json:"message"`
}

type sarifDescriptorRef struct {
	ID string `json:"id"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID     string              `json:"ruleId,omitempty"`
	Kind       string              `json:"kind"`
	Level      string              `json:"level"`
	Message    sarifMessage        `json:"message"`
	Locations  []sarifLocation     `json:"locations,omitempty"`
	Properties sarifResultProperty `json:"properties"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifactLoc `json:"artifactLocation"`
	Region           *sarifRegion     `json:"region,omitempty"`
}

type sarifArtifactLoc struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifResultProperty struct {
	Step     domain.StepName              `json:"step"`
	Source   domain.FeedbackFindingSource `json:"source,omitempty"`
	Severity domain.Severity              `json:"severity,omitempty"`
	// Located is false when warden observed no location for the result; the
	// result then carries no locations rather than an invented one.
	Located bool `json:"located"`
}

type sarifRunProperties struct {
	Feedback sarifFeedbackProperties `json:"warden"`
}

// sarifFeedbackProperties carries what SARIF has no field for: the watcher's
// state, the reader's freshness and the snapshot the results describe.
type sarifFeedbackProperties struct {
	Schema      string                   `json:"schema"`
	Mode        string                   `json:"mode"`
	State       domain.FeedbackState     `json:"state"`
	Freshness   domain.FeedbackFreshness `json:"freshness"`
	Diagnostics bool                     `json:"diagnostics"`
	Message     string                   `json:"message,omitempty"`
	Snapshot    domain.FeedbackSnapshot  `json:"snapshot"`
	EvaluatedAt *time.Time               `json:"evaluated_at"`
	Steps       []sarifStepProperty      `json:"steps"`
}

type sarifStepProperty struct {
	Step    domain.StepName           `json:"step"`
	Status  domain.FeedbackStepStatus `json:"status"`
	Summary string                    `json:"summary"`
	Blocker domain.Blocker            `json:"blocker,omitempty"`
}

// RenderSARIF writes the document as a SARIF 2.1.0 log with one run.
//
// Each finding of a failing step becomes a result. A finding warden has no
// location for keeps none: the result has no locations and
// properties.located is false, instead of an invented line. A failing step
// with no findings becomes one location-less result carrying its summary.
// A step the machine prevented from running is not a result about the code;
// it is a tool execution notification, and the invocation is marked
// unsuccessful.
//
// Freshness is stated in run.properties.warden and, when the document does
// not describe the worktree as it is now, as a notification; the results are
// then withheld (properties.warden.diagnostics is false) so a consumer never
// shows them as current.
func RenderSARIF(w io.Writer, doc domain.FeedbackDocument) error {
	show, why := Diagnosable(doc)
	inv := sarifInvocation{ExecutionSuccessful: doc.State != domain.FeedbackBlocked}
	if doc.EvaluatedAt != nil {
		inv.EndTimeUTC = doc.EvaluatedAt.UTC().Format(time.RFC3339Nano)
	}
	if why != "" {
		level := "warning"
		if show {
			level = "note"
		}
		inv.Notifications = append(inv.Notifications, notification(sarifNoteFreshness, level,
			fmt.Sprintf("%s: %s", freshnessLabel(doc), why)))
	}
	if msg := strings.TrimSpace(doc.Message); msg != "" {
		inv.Notifications = append(inv.Notifications, notification(sarifNoteState, "note", msg))
	}
	props := sarifFeedbackProperties{
		Schema:      doc.Schema,
		Mode:        doc.Mode,
		State:       doc.State,
		Freshness:   doc.Freshness,
		Diagnostics: show,
		Message:     doc.Message,
		Snapshot:    doc.Snapshot,
		EvaluatedAt: doc.EvaluatedAt,
		Steps:       []sarifStepProperty{},
	}
	results := []sarifResult{}
	rules := map[string]bool{}
	for i := range doc.Steps {
		step := &doc.Steps[i]
		props.Steps = append(props.Steps, sarifStepProperty{Step: step.Step, Status: step.Status, Summary: step.Summary, Blocker: step.Blocker})
		if step.Status != domain.FeedbackStepFail {
			continue
		}
		if blocked(step) {
			inv.ExecutionSuccessful = false
			reason := step.Blocker.Reason()
			if reason == "" {
				reason = string(step.Blocker)
			}
			inv.Notifications = append(inv.Notifications, notification(sarifNoteBlocked, "error",
				fmt.Sprintf("%s could not run (%s: %s): %s", step.Step, step.Blocker, reason, step.Summary)))
			continue
		}
		if !show {
			continue
		}
		results = append(results, stepResults(step, rules)...)
	}
	run := sarifRun{
		Tool:        sarifTool{Driver: sarifDriver{Name: "warden", Version: doc.Producer.WardenVersion, InformationURI: wardenURI}},
		Invocations: []sarifInvocation{inv},
		Results:     results,
		Properties:  sarifRunProperties{Feedback: props},
	}
	if root := worktreeURI(doc.Worktree); root != "" {
		run.OriginalURIBaseIDs = map[string]sarifArtifactLoc{sarifSourceRoot: {URI: root}}
	}
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		run.Tool.Driver.Rules = append(run.Tool.Driver.Rules, sarifRule{ID: id})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(sarifLog{Schema: sarifSchema, Version: sarifVersion, Runs: []sarifRun{run}})
}

func stepResults(step *domain.FeedbackStep, rules map[string]bool) []sarifResult {
	if len(step.Findings) == 0 {
		text := step.Summary
		if strings.TrimSpace(text) == "" {
			text = string(step.Step) + " failed"
		}
		return []sarifResult{{
			Kind:       "fail",
			Level:      "error",
			Message:    sarifMessage{Text: text},
			Properties: sarifResultProperty{Step: step.Step},
		}}
	}
	out := make([]sarifResult, 0, len(step.Findings))
	for _, f := range step.Findings {
		text := f.Message
		if strings.TrimSpace(text) == "" {
			text = step.Summary
		}
		if strings.TrimSpace(text) == "" {
			text = string(step.Step) + " failed"
		}
		r := sarifResult{
			RuleID:     f.Rule,
			Kind:       "fail",
			Level:      sarifLevel(f.Severity),
			Message:    sarifMessage{Text: text},
			Properties: sarifResultProperty{Step: step.Step, Source: f.Source, Severity: f.Severity, Located: located(f)},
		}
		if f.Rule != "" {
			rules[f.Rule] = true
		}
		if loc, ok := artifactLocation(f.File); ok {
			physical := sarifPhysical{ArtifactLocation: loc}
			if f.Line > 0 {
				physical.Region = &sarifRegion{StartLine: f.Line}
			}
			r.Locations = []sarifLocation{{PhysicalLocation: physical}}
		}
		out = append(out, r)
	}
	return out
}

func notification(id, level, text string) sarifNotification {
	return sarifNotification{Descriptor: sarifDescriptorRef{ID: id}, Level: level, Message: sarifMessage{Text: text}}
}

// sarifLevel maps warden's severities onto SARIF result levels.
func sarifLevel(s domain.Severity) string {
	switch s {
	case domain.SeverityHigh:
		return "error"
	case domain.SeverityMedium:
		return "warning"
	default:
		return "note"
	}
}

// artifactLocation turns a finding's file into a SARIF artifact location: a
// relative path is a URI reference resolved against the worktree, an
// absolute one a file URI.
func artifactLocation(file string) (sarifArtifactLoc, bool) {
	if file == "" {
		return sarifArtifactLoc{}, false
	}
	slashed := filepath.ToSlash(file)
	if filepath.IsAbs(file) || path.IsAbs(slashed) {
		return sarifArtifactLoc{URI: fileURI(slashed)}, true
	}
	return sarifArtifactLoc{URI: (&url.URL{Path: slashed}).EscapedPath(), URIBaseID: sarifSourceRoot}, true
}

// worktreeURI is the worktree as a file URI ending in a slash, which SARIF
// requires of a base URI.
func worktreeURI(worktree string) string {
	if worktree == "" {
		return ""
	}
	u := fileURI(filepath.ToSlash(worktree))
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

// fileURI builds a file URI from a slash-separated absolute path, including a
// Windows drive path ("C:/x" becomes file:///C:/x).
func fileURI(slashed string) string {
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}
