package domain

import (
	"errors"
	"fmt"
)

// Outcome classifies how a run ended. Pending is the live state; the rest are
// terminal.
type Outcome string

const (
	OutcomePending  Outcome = "pending"
	OutcomePassed   Outcome = "passed"   // all steps passed (and, for pre-push, pushed)
	OutcomeFailed   Outcome = "failed"   // a step rejected the change
	OutcomeRejected Outcome = "rejected" // an approver declined at the gate
	OutcomeAborted  Outcome = "aborted"  // operational abort (e.g. branch moved)
)

// RunID is the identity of a run aggregate. It is a value object: non-empty by
// construction so an aggregate can never exist without identity.
type RunID string

// NewRunID validates and constructs a RunID.
func NewRunID(s string) (RunID, error) {
	if s == "" {
		return "", errors.New("run id must not be empty")
	}
	return RunID(s), nil
}

// ErrRunTerminal is returned when a mutating operation is attempted on a run
// that has already reached a terminal outcome.
var ErrRunTerminal = errors.New("run is already terminal")

// Run is the aggregate root for a single gate invocation. It owns the run's
// lifecycle invariants: validation steps fold in while the run is pending; a
// failing step terminates it as Failed; once validation passes the run may
// require approval, then be pushed (Passed), rejected, or aborted. Terminal
// states are final — the aggregate rejects any further transition. The Runner
// (application service) orchestrates I/O around this aggregate; the rules about
// what may follow what live here, not in the orchestrator.
type Run struct {
	id            RunID
	hook          Hook
	policy        ResolvedPolicy
	branch        string
	results       []StepResult
	findings      []Finding
	findingKeys   map[string]bool
	needsApproval bool
	outcome       Outcome
	message       string
	record        *RunRecord
}

// NewRun starts a pending run for a resolved policy on a branch.
func NewRun(id RunID, hook Hook, policy ResolvedPolicy, branch string) *Run {
	return &Run{
		id:          id,
		hook:        hook,
		policy:      policy,
		branch:      branch,
		findingKeys: map[string]bool{},
		outcome:     OutcomePending,
	}
}

func (r *Run) ID() RunID              { return r.id }
func (r *Run) Hook() Hook             { return r.hook }
func (r *Run) Policy() ResolvedPolicy { return r.policy }
func (r *Run) Branch() string         { return r.branch }
func (r *Run) Outcome() Outcome       { return r.outcome }
func (r *Run) Message() string        { return r.message }
func (r *Run) Record() *RunRecord     { return r.record }

// Findings returns a defensive copy of the accumulated findings.
func (r *Run) Findings() []Finding {
	out := make([]Finding, len(r.findings))
	copy(out, r.findings)
	return out
}

// IsTerminal reports whether the run has reached a final outcome.
func (r *Run) IsTerminal() bool { return r.outcome != OutcomePending }

// RecordStep folds a validation step's result into the run: it accumulates the
// result and de-duplicates its findings, tracks a needs-approval request, and
// terminates the run as Failed on a StepFail. It errors if the run is already
// terminal, so a caller cannot record past a decision.
func (r *Run) RecordStep(res StepResult) error {
	if r.IsTerminal() {
		return fmt.Errorf("record step %s: %w", res.Step, ErrRunTerminal)
	}
	r.results = append(r.results, res)
	r.addFindings(res.Findings)

	switch res.Status {
	case StepFail:
		r.outcome = OutcomeFailed
		r.message = fmt.Sprintf("step %s failed", res.Step)
	case StepNeedsApproval:
		r.needsApproval = true
	}
	return nil
}

// RequiresApproval reports whether the push gate needs a human decision: a rule
// demanded approval, a step asked for it, or a blocking (high-severity) finding
// exists. This is the domain rule for the gate — not the orchestrator's.
func (r *Run) RequiresApproval() bool {
	if r.policy.RequireApproval || r.needsApproval {
		return true
	}
	for _, f := range r.findings {
		if f.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// Pass marks a pending run as Passed with no push (the pre-commit terminal).
func (r *Run) Pass() error {
	if err := r.ensurePending(); err != nil {
		return err
	}
	r.outcome = OutcomePassed
	return nil
}

// MarkPushed marks a pending run Passed and attaches its provenance record (the
// pre-push terminal after a successful push).
func (r *Run) MarkPushed(rec RunRecord, message string) error {
	if err := r.ensurePending(); err != nil {
		return err
	}
	r.outcome = OutcomePassed
	r.record = &rec
	r.message = message
	return nil
}

// Reject marks a pending run declined at the approval gate.
func (r *Run) Reject(reason string) error {
	if err := r.ensurePending(); err != nil {
		return err
	}
	r.outcome = OutcomeRejected
	r.message = reason
	return nil
}

// Abort marks a pending run as an operational abort (e.g. the branch moved).
func (r *Run) Abort(reason string) error {
	if err := r.ensurePending(); err != nil {
		return err
	}
	r.outcome = OutcomeAborted
	r.message = reason
	return nil
}

func (r *Run) ensurePending() error {
	if r.IsTerminal() {
		return ErrRunTerminal
	}
	return nil
}

func (r *Run) addFindings(fs []Finding) {
	for _, f := range fs {
		key := fmt.Sprintf("%s:%d:%s", f.File, f.Line, f.Message)
		if r.findingKeys[key] {
			continue
		}
		r.findingKeys[key] = true
		r.findings = append(r.findings, f)
	}
}
