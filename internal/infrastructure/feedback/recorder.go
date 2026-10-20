package feedback

import (
	"context"
	"sync"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Recorder builds the feedback document from a staged watcher's evaluations
// and writes it on every transition and heartbeat. It is the document's only
// writer: gate runs never construct one.
//
// It is an application.Observer for the step events of the evaluation that
// Begin started; events that arrive after that evaluation's context ends
// (superseded or stopped) are dropped, so a result can only land on the
// snapshot it was produced for.
type Recorder struct {
	path string
	now  func() time.Time

	mu  sync.Mutex
	doc domain.FeedbackDocument
	run context.Context
	// begun is when each step started in the current evaluation.
	begun map[domain.StepName]time.Time
	// lastErr is the last write's error; the next transition or heartbeat
	// tries again.
	lastErr error
}

// RecorderOptions describe the producer.
type RecorderOptions struct {
	Path          string
	WardenVersion string
	Mode          string
	Worktree      string
	StartedAt     time.Time
	PID           int
}

// NewRecorder starts a document in the pending state. Nothing is written
// until the first snapshot is known.
func NewRecorder(o RecorderOptions) *Recorder {
	return &Recorder{
		path: o.Path,
		now:  func() time.Time { return time.Now().UTC() },
		doc: domain.FeedbackDocument{
			Schema: domain.FeedbackSchema,
			Producer: domain.FeedbackProducer{
				WardenVersion: o.WardenVersion,
				PID:           o.PID,
				StartedAt:     o.StartedAt.UTC(),
			},
			Mode:     o.Mode,
			Worktree: o.Worktree,
			State:    domain.FeedbackPending,
			Steps:    []domain.FeedbackStep{},
		},
		begun: map[domain.StepName]time.Time{},
	}
}

// Begin starts an evaluation of snapshot. The step list is replaced with a
// pending entry for each expected step: results never carry over to a new
// evaluation, so none can appear under a snapshot it was not produced for.
func (r *Recorder) Begin(run context.Context, snapshot domain.FeedbackSnapshot, expected []domain.StepName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = run
	r.begun = map[domain.StepName]time.Time{}
	r.doc.Snapshot = snapshot
	r.doc.State = domain.FeedbackRunning
	r.doc.Message = ""
	r.doc.EvaluatedAt = nil
	r.doc.Freshness = ""
	r.doc.Steps = make([]domain.FeedbackStep, 0, len(expected))
	for _, name := range expected {
		r.doc.Steps = append(r.doc.Steps, pendingStep(name))
	}
	r.writeLocked()
}

func pendingStep(name domain.StepName) domain.FeedbackStep {
	return domain.FeedbackStep{Step: name, Status: domain.FeedbackStepPending, Findings: []domain.FeedbackFinding{}}
}

// OnStep records a step event of the current evaluation.
func (r *Recorder) OnStep(e application.StepEvent) {
	if e.Phase != application.StepStarted && e.Phase != application.StepFinished {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == nil || r.run.Err() != nil || r.doc.State != domain.FeedbackRunning {
		return
	}
	now := r.now()
	var step domain.FeedbackStep
	if e.Phase == application.StepStarted {
		r.begun[e.Step] = now
		step = pendingStep(e.Step)
		step.Status = domain.FeedbackStepRunning
		step.StartedAt = &now
	} else {
		result := e.Result
		if result.Step == "" {
			result.Step = e.Step
		}
		// A pass whose execution contract names another tree checked a
		// different snapshot (the index moved between the snapshot and the
		// worktree seed). It is not recorded against this one; the watcher
		// re-evaluates the new snapshot anyway.
		if result.Contract != nil && result.Contract.TreeSHA != r.doc.Snapshot.Tree {
			return
		}
		step = domain.FeedbackStepFromResult(result, r.begun[e.Step], now)
	}
	r.setStep(step)
	r.writeLocked()
}

func (r *Recorder) setStep(step domain.FeedbackStep) {
	for i := range r.doc.Steps {
		if r.doc.Steps[i].Step == step.Step {
			r.doc.Steps[i] = step
			return
		}
	}
	r.doc.Steps = append(r.doc.Steps, step)
}

// Complete records that the current evaluation finished. message is empty
// for a pass. Steps that never started are dropped: listing them as pending
// would claim work that is not coming.
func (r *Recorder) Complete(message string) {
	r.finish(domain.FeedbackComplete, message, true)
}

// Idle records that the current snapshot has nothing to check.
func (r *Recorder) Idle(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = nil
	r.doc.State = domain.FeedbackIdle
	r.doc.Message = message
	now := r.now()
	r.doc.EvaluatedAt = &now
	r.doc.Steps = []domain.FeedbackStep{}
	r.writeLocked()
}

// Blocked records that the machine prevented the evaluation. Results a
// partial run did observe stay; they describe this snapshot.
func (r *Recorder) Blocked(message string) {
	r.finish(domain.FeedbackBlocked, message, false)
}

// Paused records that the evaluation was cancelled and nothing runs until
// the reason (message) clears, e.g. a gate run in progress.
func (r *Recorder) Paused(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = nil
	r.doc.State = domain.FeedbackPending
	r.doc.Message = message
	r.unstartLocked()
	r.writeLocked()
}

func (r *Recorder) finish(state domain.FeedbackState, message string, evaluated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = nil
	r.doc.State = state
	r.doc.Message = message
	if evaluated {
		now := r.now()
		r.doc.EvaluatedAt = &now
	}
	kept := r.doc.Steps[:0]
	for i := range r.doc.Steps {
		s := &r.doc.Steps[i]
		switch s.Status {
		case domain.FeedbackStepPending:
			continue
		case domain.FeedbackStepRunning:
			s.Status, s.StartedAt = domain.FeedbackStepPending, nil
		}
		kept = append(kept, *s)
	}
	r.doc.Steps = kept
	r.writeLocked()
}

// unstartLocked turns running steps back into pending ones: their run was
// abandoned and will never finish.
func (r *Recorder) unstartLocked() {
	for i := range r.doc.Steps {
		if r.doc.Steps[i].Status == domain.FeedbackStepRunning {
			r.doc.Steps[i] = pendingStep(r.doc.Steps[i].Step)
		}
	}
}

// EnvironmentObserved tells the recorder which environment the watcher
// sampled most recently. When it differs from the one the snapshot was
// evaluated under, the document says stale_environment until an evaluation
// of the new environment begins. Only a producer can know this.
func (r *Recorder) EnvironmentObserved(digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.doc.Snapshot.ID == "" || digest == "" {
		return
	}
	freshness := domain.FeedbackFreshness("")
	if digest != r.doc.Snapshot.EnvironmentDigest {
		freshness = domain.FreshnessStaleEnvironment
	}
	if freshness != r.doc.Freshness {
		r.doc.Freshness = freshness
		r.writeLocked()
	}
}

// Heartbeat refreshes producer.heartbeat_at and rewrites the document, which
// also retries a write that failed earlier.
func (r *Recorder) Heartbeat() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeLocked()
	return r.lastErr
}

// Stop records a clean exit: state stopped, and no step left running.
func (r *Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = nil
	r.doc.State = domain.FeedbackStopped
	r.unstartLocked()
	r.writeLocked()
	return r.lastErr
}

// Document returns a copy of the current document.
func (r *Recorder) Document() domain.FeedbackDocument {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc := r.doc
	doc.Steps = append([]domain.FeedbackStep(nil), r.doc.Steps...)
	return doc
}

// writeLocked writes the document once a snapshot is known. A failed write
// is kept as lastErr and retried by the next transition or heartbeat.
func (r *Recorder) writeLocked() {
	if r.doc.Snapshot.ID == "" {
		return
	}
	r.doc.Producer.HeartbeatAt = r.now()
	r.lastErr = Write(r.path, r.doc)
}
