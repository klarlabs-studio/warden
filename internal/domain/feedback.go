package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The background feedback document (warden.feedback/v1) is what the staged
// watcher last observed about one worktree, for editors and other readers.
//
// It is advisory and unsigned, and that is deliberate: it is not evidence.
// The gate, verify, attest, notes and execution requirements never read it.
// Two rules follow from that:
//
//   - Freshness is computed by the reader, not asserted by the writer. A
//     producer can only say what it observed and when; whether that still
//     describes the repository is a question about now. The one exception is
//     FreshnessStaleEnvironment, which only the producer can see, because a
//     reader never probes a container engine.
//   - Steps describe only Snapshot.ID. When the snapshot changes, the writer
//     replaces the step list with pending entries; a result is never carried
//     forward under a new id.

// FeedbackSchema identifies version 1 of the document.
const FeedbackSchema = "warden.feedback/v1"

// FeedbackHeartbeatInterval is how often a running producer refreshes
// producer.heartbeat_at. A reader treats a heartbeat older than
// FeedbackProducerTimeout as a producer that is gone.
const (
	FeedbackHeartbeatInterval = 5 * time.Second
	FeedbackProducerTimeout   = 3 * FeedbackHeartbeatInterval
)

// FeedbackMessageLimit caps one finding's message in bytes. A longer message
// is cut at a UTF-8 boundary and the step's output_truncated is set.
const FeedbackMessageLimit = 64 << 10

// FeedbackModeStaged is the staged watcher's mode; FeedbackModeLive is
// reserved for the container-aware live watcher.
const (
	FeedbackModeStaged = "staged"
	FeedbackModeLive   = "live"
)

// FeedbackState is what the producer was doing about the snapshot.
type FeedbackState string

const (
	// FeedbackIdle: the snapshot has nothing to check (nothing staged).
	FeedbackIdle FeedbackState = "idle"
	// FeedbackPending: the snapshot is known but no evaluation is running
	// (debouncing, or paused while a gate run is in progress).
	FeedbackPending FeedbackState = "pending"
	// FeedbackRunning: an evaluation of the snapshot is in progress.
	FeedbackRunning FeedbackState = "running"
	// FeedbackComplete: an evaluation of the snapshot finished.
	FeedbackComplete FeedbackState = "complete"
	// FeedbackBlocked: the machine prevented the evaluation (engine down, a
	// tool's lock held elsewhere); it is retried. Not a verdict.
	FeedbackBlocked FeedbackState = "blocked"
	// FeedbackStopped: the producer exited cleanly. Steps keep what it last
	// observed for the snapshot; none is still running.
	FeedbackStopped FeedbackState = "stopped"
)

// FeedbackStepStatus is one step's status within the snapshot.
type FeedbackStepStatus string

const (
	FeedbackStepPending FeedbackStepStatus = "pending"
	FeedbackStepRunning FeedbackStepStatus = "running"
	FeedbackStepPass    FeedbackStepStatus = "pass"
	FeedbackStepFail    FeedbackStepStatus = "fail"
	// FeedbackStepCached means a staged prediction existed for this snapshot
	// when the producer ran the step. It is not a promise that the gate will
	// skip it: the gate re-derives that itself.
	FeedbackStepCached FeedbackStepStatus = "cached"
	// FeedbackStepSkipped means nothing was configured to perform the step.
	FeedbackStepSkipped FeedbackStepStatus = "skipped"
)

// FeedbackFreshness is the reader's judgement of whether the document still
// describes the repository.
type FeedbackFreshness string

const (
	// FreshnessCurrent: the document describes the repository as it is now.
	FreshnessCurrent FeedbackFreshness = "current"
	// FreshnessStaleSnapshot: the staged tree, HEAD, branch or configuration
	// moved since the producer observed them.
	FreshnessStaleSnapshot FeedbackFreshness = "stale_snapshot"
	// FreshnessStaleEnvironment: the producer saw the container environment
	// change after the snapshot was evaluated. Only a producer writes it.
	FreshnessStaleEnvironment FeedbackFreshness = "stale_environment"
	// FreshnessProducerGone: a producer that should still be running stopped
	// heartbeating, or its process no longer exists.
	FreshnessProducerGone FeedbackFreshness = "producer_gone"
	// FreshnessExpired: a passing step's staged prediction is past its TTL.
	FreshnessExpired FeedbackFreshness = "expired"
)

// FeedbackFindingSource says where a finding came from.
type FeedbackFindingSource string

const (
	// FindingSourceStep: a structured finding a step reported.
	FindingSourceStep FeedbackFindingSource = "step"
	// FindingSourceSARIF: a result read from a SARIF file the check wrote.
	FindingSourceSARIF FeedbackFindingSource = "sarif"
	// FindingSourceOutput: the check's verbatim output. It has no location,
	// because warden does not guess one from a tool's prose.
	FindingSourceOutput FeedbackFindingSource = "output"
)

// FeedbackDocument is the warden.feedback/v1 document.
type FeedbackDocument struct {
	Schema   string           `json:"schema"`
	Producer FeedbackProducer `json:"producer"`
	Mode     string           `json:"mode"`
	Worktree string           `json:"worktree"`
	State    FeedbackState    `json:"state"`
	Message  string           `json:"message"`
	Snapshot FeedbackSnapshot `json:"snapshot"`
	// EvaluatedAt is when an evaluation of the snapshot last ended (complete
	// or idle); null while none has.
	EvaluatedAt *time.Time     `json:"evaluated_at"`
	Steps       []FeedbackStep `json:"steps"`
	// Freshness is filled in by the reader. A producer may write only
	// FreshnessStaleEnvironment here.
	Freshness FeedbackFreshness `json:"freshness,omitempty"`
}

// FeedbackProducer identifies the process that wrote the document.
type FeedbackProducer struct {
	WardenVersion string    `json:"warden_version"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	HeartbeatAt   time.Time `json:"heartbeat_at"`
}

// FeedbackSnapshot is what an evaluation checks. ID covers the parts a reader
// can recompute without a container engine; the environment is recorded
// beside it as EnvironmentDigest.
type FeedbackSnapshot struct {
	ID string `json:"id"`
	// Tree is the tree the index would write (`git write-tree`).
	Tree string `json:"tree"`
	// Base is HEAD's commit; empty on an unborn branch.
	Base string `json:"base"`
	// Branch is HEAD's abbreviated name ("HEAD" when detached).
	Branch              string `json:"branch"`
	ConfigurationDigest string `json:"configuration_digest"`
	// EnvironmentDigest identifies the sampled container environment the
	// evaluation started under; empty when none was observed.
	EnvironmentDigest string `json:"environment_digest"`
}

// FeedbackStep is one step's status for the snapshot.
type FeedbackStep struct {
	Step       StepName           `json:"step"`
	Status     FeedbackStepStatus `json:"status"`
	Summary    string             `json:"summary"`
	Blocker    Blocker            `json:"blocker"`
	StartedAt  *time.Time         `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at"`
	// Environment is present only for observed container execution.
	Environment     *ExecutionEnvironment `json:"environment,omitempty"`
	Prediction      FeedbackPrediction    `json:"prediction"`
	Findings        []FeedbackFinding     `json:"findings"`
	OutputTruncated bool                  `json:"output_truncated"`
}

// FeedbackPrediction says whether the step's pass was saved as a staged
// prediction (or served from one), and when that prediction expires.
type FeedbackPrediction struct {
	Saved     bool       `json:"saved"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// FeedbackFinding is a domain Finding plus where it came from.
type FeedbackFinding struct {
	Finding
	Source FeedbackFindingSource `json:"source"`
}

// FeedbackSnapshotID derives a snapshot's id from the parts a reader can
// recompute.
func FeedbackSnapshotID(tree, base, branch, configurationDigest string) string {
	id, _ := DigestJSON(struct {
		Schema        string `json:"schema"`
		Tree          string `json:"tree"`
		Base          string `json:"base"`
		Branch        string `json:"branch"`
		Configuration string `json:"configuration_digest"`
	}{FeedbackSchema, tree, base, branch, configurationDigest})
	return id
}

// NewFeedbackSnapshot builds a snapshot with its id.
func NewFeedbackSnapshot(tree, base, branch, configurationDigest, environmentDigest string) FeedbackSnapshot {
	return FeedbackSnapshot{
		ID:                  FeedbackSnapshotID(tree, base, branch, configurationDigest),
		Tree:                tree,
		Base:                base,
		Branch:              branch,
		ConfigurationDigest: configurationDigest,
		EnvironmentDigest:   environmentDigest,
	}
}

// ErrInvalidFeedback marks a document that is not a valid warden.feedback/v1.
var ErrInvalidFeedback = errors.New("invalid feedback document")

// Validate checks the document against the v1 contract.
func (d FeedbackDocument) Validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidFeedback, fmt.Sprintf(format, args...))
	}
	if d.Schema != FeedbackSchema {
		return invalid("schema %q, want %q", d.Schema, FeedbackSchema)
	}
	if d.Mode != FeedbackModeStaged && d.Mode != FeedbackModeLive {
		return invalid("unknown mode %q", d.Mode)
	}
	if d.Producer.PID <= 0 || d.Producer.StartedAt.IsZero() || d.Producer.HeartbeatAt.IsZero() {
		return invalid("producer must name a pid, a start time and a heartbeat")
	}
	switch d.State {
	case FeedbackIdle, FeedbackPending, FeedbackRunning, FeedbackComplete, FeedbackBlocked, FeedbackStopped:
	default:
		return invalid("unknown state %q", d.State)
	}
	if d.Freshness != "" && d.Freshness != FreshnessStaleEnvironment {
		return invalid("a producer may not assert freshness %q", d.Freshness)
	}
	s := d.Snapshot
	if !ValidTreeSHA(s.Tree) || (s.Base != "" && !ValidTreeSHA(s.Base)) || !strings.HasPrefix(s.ConfigurationDigest, "sha256:") {
		return invalid("snapshot must name a tree, a base and a configuration digest")
	}
	if s.ID != FeedbackSnapshotID(s.Tree, s.Base, s.Branch, s.ConfigurationDigest) {
		return invalid("snapshot id does not match its parts")
	}
	seen := make(map[StepName]bool, len(d.Steps))
	for i := range d.Steps {
		step := &d.Steps[i]
		if err := step.validate(); err != nil {
			return invalid("step %q: %v", step.Step, err)
		}
		if seen[step.Step] {
			return invalid("step %q listed twice", step.Step)
		}
		seen[step.Step] = true
	}
	return nil
}

func (s *FeedbackStep) validate() error {
	if s.Step == "" {
		return errors.New("no name")
	}
	switch s.Status {
	case FeedbackStepPending, FeedbackStepRunning, FeedbackStepPass, FeedbackStepFail, FeedbackStepCached, FeedbackStepSkipped:
	default:
		return fmt.Errorf("unknown status %q", s.Status)
	}
	for _, f := range s.Findings {
		switch f.Source {
		case FindingSourceStep, FindingSourceSARIF, FindingSourceOutput:
		default:
			return fmt.Errorf("unknown finding source %q", f.Source)
		}
	}
	return nil
}

// FeedbackObservation is what a reader observed now, outside the document.
type FeedbackObservation struct {
	Now time.Time
	// CurrentSnapshotID is the snapshot id recomputed from the repository.
	CurrentSnapshotID string
	// ProducerAlive reports whether the producer's pid names a live process.
	ProducerAlive bool
}

// ComputeFreshness judges whether the document still describes the repository. The
// first rule that applies wins:
//
//  1. stale_snapshot: the recomputed snapshot id differs. Results for another
//     snapshot say nothing about this one, whoever produced them.
//  2. producer_gone: the producer has not stopped cleanly, yet its heartbeat
//     is older than FeedbackProducerTimeout or its process is gone. A
//     "running" step it left behind will never finish.
//  3. stale_environment: the producer saw the environment change.
//  4. expired: a passing step's staged prediction is past its expiry.
//  5. current.
func (d FeedbackDocument) ComputeFreshness(obs FeedbackObservation) FeedbackFreshness {
	if obs.CurrentSnapshotID != d.Snapshot.ID {
		return FreshnessStaleSnapshot
	}
	if d.State != FeedbackStopped && (!obs.ProducerAlive || obs.Now.Sub(d.Producer.HeartbeatAt) > FeedbackProducerTimeout) {
		return FreshnessProducerGone
	}
	if d.Freshness == FreshnessStaleEnvironment {
		return FreshnessStaleEnvironment
	}
	for i := range d.Steps {
		step := &d.Steps[i]
		passed := step.Status == FeedbackStepPass || step.Status == FeedbackStepCached
		if passed && step.Prediction.ExpiresAt != nil && obs.Now.After(*step.Prediction.ExpiresAt) {
			return FreshnessExpired
		}
	}
	return FreshnessCurrent
}

// FeedbackStepFromResult renders a finished step for the document. started
// and finished are when the producer saw the step start and finish.
func FeedbackStepFromResult(res StepResult, started, finished time.Time) FeedbackStep {
	step := FeedbackStep{
		Step:        res.Step,
		Summary:     res.Summary,
		Blocker:     res.Blocker,
		FinishedAt:  &finished,
		Environment: res.Environment,
		Findings:    []FeedbackFinding{},
	}
	if !started.IsZero() {
		step.StartedAt = &started
	}
	switch {
	case res.Status != StepPass:
		step.Status = FeedbackStepFail
	case res.Reuse != nil && res.Reuse.Source == PredictionPurpose:
		step.Status = FeedbackStepCached
		expires := res.Reuse.RecordedAt.Add(PredictionTTL).UTC()
		step.Prediction.ExpiresAt = &expires
	case res.Outcome == StepSkipped:
		step.Status = FeedbackStepSkipped
	default:
		step.Status = FeedbackStepPass
	}
	if step.Status == FeedbackStepPass && !res.PredictionSavedAt.IsZero() {
		expires := res.PredictionSavedAt.Add(PredictionTTL).UTC()
		step.Prediction = FeedbackPrediction{Saved: true, ExpiresAt: &expires}
	}
	for _, f := range res.Findings {
		message, cut := truncateUTF8(f.Message, FeedbackMessageLimit)
		f.Message = message
		step.OutputTruncated = step.OutputTruncated || cut
		step.Findings = append(step.Findings, FeedbackFinding{Finding: f, Source: findingSource(f)})
	}
	return step
}

// findingSource tells a structured finding from a command's verbatim output.
// A failing shell check reports one location-less finding whose message is
// the tool's own output, with no rule or with one of warden's step/* rules
// describing how the command ended.
func findingSource(f Finding) FeedbackFindingSource {
	if f.FromSARIF {
		return FindingSourceSARIF
	}
	if f.File == "" && f.Line == 0 && (f.Rule == "" || strings.HasPrefix(f.Rule, "step/")) {
		return FindingSourceOutput
	}
	return FindingSourceStep
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune.
func truncateUTF8(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
