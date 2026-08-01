package mcpserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// Asynchronous runs.
//
// run_trigger used to block until the whole pipeline finished. On a repo whose
// test step takes five minutes that is a five-minute silent tool call, and most
// MCP clients time out long before it returns — so the one operation that
// actually gates a push was the one an agent could least reliably use. Worse,
// when the client gave up there was no way to ask what had happened: the run
// carried on invisibly and its result went nowhere.
//
// So a run now starts in the background and returns a handle. run_status polls
// it and reports the steps that have finished so far, which is also strictly
// more information than the old synchronous call ever gave: an agent can watch
// `lint` pass and `test` still running instead of staring at a blank call.
//
// The registry is per-server and in-memory. A run belongs to the process that
// started it — warden is a local gate, not a job service — and losing run
// history when the server exits is correct rather than unfortunate: a run whose
// server is gone has no result to collect.

// RunPhase is a run's lifecycle state as reported by run_status.
type RunPhase string

const (
	// RunRunning means the pipeline is still executing. Steps reported so far
	// are final; the run's outcome is not yet known.
	RunRunning RunPhase = "running"
	// RunComplete means the pipeline finished and Summary carries the verdict.
	// It does NOT mean the gate passed — read Summary.Outcome for that.
	RunComplete RunPhase = "complete"
	// RunErrored means the run could not be carried out at all (the pipeline
	// itself failed, not the change). There is no Summary to read.
	RunErrored RunPhase = "errored"
)

// StepProgress is one finished step, as reported to a polling caller.
//
// Only finished steps are recorded. The output-line events the live TUI renders
// are explicitly best-effort and may be dropped under load, so accumulating them
// here would hand a polling agent a partial transcript it might reasonably
// mistake for the whole one.
type StepProgress struct {
	Step   domain.StepName `json:"step"`
	Status string          `json:"status"` // pass | fail | needs_approval
	// Summary is the step's own one-line verdict, when it gave one.
	Summary string `json:"summary,omitempty"`
	// Findings are what the step reported. They arrive here as soon as the step
	// finishes rather than only in the final summary, so an agent can start on a
	// fix while later steps are still running.
	Findings []domain.Finding `json:"findings,omitempty"`
}

// RunStatusOutput is what run_status returns.
type RunStatusOutput struct {
	RunID string   `json:"run_id"`
	Phase RunPhase `json:"phase"`
	Hook  string   `json:"hook"`
	// Steps are the steps that have finished, in completion order.
	Steps []StepProgress `json:"steps"`
	// Summary is present only when Phase is complete.
	Summary *RunSummary `json:"summary,omitempty"`
	// Error is present only when Phase is errored.
	Error string `json:"error,omitempty"`
}

// runState is one tracked run. Every field behind mu is written by the run's
// goroutine and read by pollers, so all access goes through the lock.
type runState struct {
	mu      sync.Mutex
	id      string
	hook    domain.Hook
	phase   RunPhase
	steps   []StepProgress
	summary *RunSummary
	err     error
	started time.Time
}

func (r *runState) recordStep(p StepProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, p)
}

func (r *runState) finish(summary RunSummary, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.phase = RunErrored
		r.err = err
		return
	}
	r.phase = RunComplete
	r.summary = &summary
}

// snapshot copies the run's observable state under the lock. Callers must never
// hold a reference into runState: the run goroutine keeps writing to it.
func (r *runState) snapshot() RunStatusOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := RunStatusOutput{
		RunID: r.id,
		Phase: r.phase,
		Hook:  string(r.hook),
		Steps: append([]StepProgress(nil), r.steps...),
	}
	if r.summary != nil {
		s := *r.summary
		out.Summary = &s
	}
	if r.err != nil {
		out.Error = r.err.Error()
	}
	return out
}

// registry tracks in-flight and finished runs for one server.
type registry struct {
	mu   sync.Mutex
	runs map[string]*runState
	seq  int
}

func newRegistry() *registry { return &registry{runs: map[string]*runState{}} }

// start allocates a run, launches it, and returns its state immediately.
//
// The run deliberately does NOT inherit the calling tool's context. An MCP
// request context is cancelled when that request returns, which for an async
// start is immediately — inheriting it would cancel the pipeline the instant
// run_trigger replied. The run is instead bounded by its own steps' timeouts,
// exactly as a run started from the CLI is.
func (g *registry) start(f Facade, hook domain.Hook) *runState {
	g.mu.Lock()
	g.seq++
	st := &runState{
		id:      fmt.Sprintf("run-%d", g.seq),
		hook:    hook,
		phase:   RunRunning,
		started: time.Now(),
	}
	g.runs[st.id] = st
	g.mu.Unlock()

	go func() {
		summary, err := f.RunTriggerStreaming(context.Background(), hook, st.recordStep)
		st.finish(summary, err)
	}()
	return st
}

// lookup returns the run with id, or nil.
func (g *registry) lookup(id string) *runState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs[id]
}
