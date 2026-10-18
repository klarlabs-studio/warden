package domain

import "strconv"

// The types in this file are the signed observations a RunRecord carries when a
// compliance profile is in force (ADR 0007, L2). They sit inside the signing
// payload, so each one is a statement the signer vouches for. Every field must
// therefore say only what warden itself observed during the run; anything it
// did not observe is left out rather than defaulted to a reassuring value.
//
// They are emitted ONLY when a profile is active. A note written without a
// profile stays byte-identical to one written before these types existed, so a
// verifier that predates them keeps verifying it. A verifier that predates them
// and meets a profiled note drops the fields it does not know, recomputes a
// different payload and fails the signature: a pinned older verifier rejects
// what it cannot read instead of accepting a claim it cannot check.

// ProfileRef names the compliance profile a run was gated under.
type ProfileRef struct {
	// Name is the level, e.g. "strict".
	Name string `json:"name"`
	// Version is the definition version; a bare `profile: strict` is 1.
	Version int `json:"version"`
	// Digest is ComplianceProfile.Digest of the definition the writing warden
	// had built in. A verifier compares it with its own table for Name@Version,
	// so a fork that redefines a published version does not pass as it.
	Digest string `json:"digest"`
}

// NewProfileRef records the profile p.
func NewProfileRef(p ComplianceProfile) ProfileRef {
	return ProfileRef{Name: p.Name(), Version: p.Version(), Digest: p.Digest()}
}

// String renders the reference as "name@version".
func (r ProfileRef) String() string {
	return r.Name + "@" + strconv.Itoa(r.Version)
}

// StepPredicted is a step whose pass was taken from a matching staged
// prediction (see Prediction) instead of running now. It is distinct from
// StepCached, the host input cache: the two rest on different evidence, and a
// verifier may weigh them differently. Delivery surfaces show both as cached;
// only the signed record keeps them apart.
const StepPredicted StepOutcome = "predicted"

// StepObservation is what warden observed of one step in a run.
//
// It deliberately carries no isolation or tree-guard claim. Agent steps are not
// yet isolated from the canonical worktree, so a field saying they were would be
// false; those observations arrive with the code that makes them true.
type StepObservation struct {
	// Outcome is executed, cached, predicted or skipped. It is the observed
	// fact, not the verdict: a skipped step still passed the gate.
	Outcome StepOutcome `json:"outcome"`
	// Agent is the coding agent warden invoked for the step. Empty when no
	// agent ran it now: not an agent step, or an agent step that was skipped
	// or cached. The agent the policy merely assigned is in RunRecord.Agent.
	Agent string `json:"agent,omitempty"`
}

// ObservedOutcome is the outcome the signed record states for a step result.
// It is ExecutionOutcome with prediction reuse named as predicted rather than
// folded into cached.
func (r StepResult) ObservedOutcome() StepOutcome {
	if r.Reuse != nil && r.Reuse.Source == PredictionPurpose {
		return StepPredicted
	}
	return r.ExecutionOutcome()
}

// ApprovalChannel names how the run's approval gate was answered.
type ApprovalChannel string

const (
	// ApprovalNone: no approval was required, so no approver was consulted.
	ApprovalNone ApprovalChannel = "none"
	// ApprovalAuto: an approver that answers without asking anyone (the agent
	// surfaces) approved. Nobody was asked.
	ApprovalAuto ApprovalChannel = "auto"
	// ApprovalTTY: an answer was read from an interactive terminal. It says a
	// terminal answered, not that a person did; an agent can drive a pty.
	ApprovalTTY ApprovalChannel = "tty"
	// ApprovalTUI: the answer was given in warden's interactive TUI. The same
	// caveat as ApprovalTTY applies.
	ApprovalTUI ApprovalChannel = "tui"
)

// ApprovalRef records the run's approval gate.
type ApprovalRef struct {
	// Required is true when the gate needed a decision: a rule required
	// approval, a step asked for it, or a blocking finding was raised.
	Required bool `json:"required"`
	// Channel is how the decision was made; ApprovalNone when Required is false.
	Channel ApprovalChannel `json:"channel"`
	// Principal is the name the approver gave itself (e.g. "warden-cli"). It is
	// a label chosen by the approving code, not an authenticated identity, and
	// is empty when no approver was consulted.
	Principal string `json:"principal,omitempty"`
}
