package domain

import "time"

// Severity classifies a finding.
type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// Finding is a single issue reported by a step. Mirrors the custom-step wire
// schema (§6) so built-in and subprocess steps produce the same shape.
//
// The fields below Line are the REMEDIATION half, and they exist because a
// finding that only describes a problem leaves its reader to rediscover the
// answer the step already had. A human reads "line too long" and goes looking
// for the formatter; an agent reads it, guesses, and re-runs the whole gate to
// learn whether the guess was right. The step usually knows which rule fired,
// what breaks if it is ignored, and the command that resolves it — so say all
// three rather than making every reader derive them again.
//
// All of them are optional. A step reporting only severity and message stays as
// valid as it was, which is what keeps existing subprocess steps working.
type Finding struct {
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	// Rule is the identifier the tool fired, e.g. "SA4006" or "no-unused-vars".
	// It is what a reader searches for, waives, or baselines — none of which
	// prose supports.
	Rule string `json:"rule,omitempty"`
	// Why explains what breaks if this is ignored. Severity RANKS a finding;
	// only this justifies it, and an unjustified finding is the kind people
	// learn to scroll past.
	Why string `json:"why,omitempty"`
	// Fix carries the remediation, when the step knows one.
	Fix *Fix `json:"fix,omitempty"`
	// FromSARIF marks a finding read from a SARIF file the step's command
	// wrote (Config.Diagnostics), rather than one the step reported itself.
	// It only lets delivery surfaces (the feedback document's source) say
	// where the finding came from; it is not serialized and is not part of
	// the evidence chain.
	FromSARIF bool `json:"-"`
}

// Fix is a remediation a step offers for a finding: a command to run, a patch to
// apply, or neither.
//
// Both forms are ADVISORY. Warden never applies a Fix on the strength of the
// finding alone — folding changes back into the tree is a policy decision
// expressed by an `auto_fix` budget, and a step must not be able to escalate
// itself into a tree write just by attaching a patch. What this does is close
// the loop for the reader: an agent can apply it and re-run, a human can paste
// it.
type Fix struct {
	// Command is a shell command that resolves the finding, e.g.
	// "gofmt -w internal/foo.go". It should be runnable from the repo root and
	// should fix only what the finding describes.
	Command string `json:"command,omitempty"`
	// Patch is a unified diff resolving the finding. Preferred over Command when
	// the change is known exactly, because it can be reviewed before it is
	// applied rather than only after.
	Patch string `json:"patch,omitempty"`
}

// Actionable reports whether a finding carries a remediation its reader can act
// on, as opposed to only a description of the problem.
func (f Finding) Actionable() bool {
	return f.Fix != nil && (f.Fix.Command != "" || f.Fix.Patch != "")
}

// StepStatus is the outcome a step reports.
type StepStatus string

const (
	StepPass          StepStatus = "pass"
	StepFail          StepStatus = "fail"
	StepNeedsApproval StepStatus = "needs_approval"
)

// Blocker classifies a step failure whose cause is the MACHINE rather than the
// change under gate. A step that never ran has not judged the tree, and saying
// "step lint failed" about it sends the developer hunting a lint error that does
// not exist. The gate still fails — "I could not check" is not "the tree is
// clean" — but the verdict must name the real obstacle, and automation must be
// able to tell the two apart without parsing prose.
type Blocker string

const (
	// BlockerNone is the ordinary case: the step ran and rejected the change.
	BlockerNone Blocker = ""
	// BlockerContention means the tool refused to start because another process
	// holds its machine-global lock (golangci-lint, cargo, …). Nothing is wrong
	// with the tree and nothing needs fixing: retrying later is the remedy.
	BlockerContention Blocker = "contention"
	// BlockerEnvironment means the command's toolchain or dependencies are not
	// installed in the checkout (`sh: astro: command not found`). Retrying
	// changes nothing; a human must run the remediation first.
	BlockerEnvironment Blocker = "environment"
	// BlockerConfig means the repository's own configuration cannot satisfy
	// what its compliance profile requires of the step: an agent step the
	// profile requires to run has no agent or command to run it. Retrying
	// changes nothing; the config must change.
	BlockerConfig Blocker = "config"
)

// Retryable reports whether re-running the same step unchanged could plausibly
// succeed. It is the machine-readable half of the distinction the exit codes
// expose: contention clears on its own, a missing toolchain does not.
func (b Blocker) Retryable() bool { return b == BlockerContention }

// blockerReasons render a blocker as the clause that completes
// "step <name> could not run: …" in a run-level verdict.
var blockerReasons = map[Blocker]string{
	BlockerContention:  "another process holds its lock",
	BlockerEnvironment: "its toolchain or dependencies are not installed",
	BlockerConfig:      "its compliance profile cannot run it as configured",
}

// Reason is the short human explanation of why the step never ran, or "" for
// BlockerNone (where there is nothing to explain — the step ran).
func (b Blocker) Reason() string { return blockerReasons[b] }

// StepResult is the normalized outcome of running one step, whether native or
// subprocess-backed.
type StepResult struct {
	Environment *ExecutionEnvironment
	Contract    *ExecutionContract
	Reuse       *ExecutionReuse
	Step        StepName
	Status      StepStatus
	Findings    []Finding
	// Fixed reports whether the step mutated the worktree (auto-fix applied).
	Fixed bool
	// Summary is a short human line for TUI/CLI output.
	Summary string
	// Blocker, when set, says the step FAILED WITHOUT RUNNING for an
	// environmental reason. It is meaningful only alongside StepFail.
	Blocker Blocker
	// Outcome says whether the step actually EXECUTED, or passed without doing
	// its work — served from the input cache, or skipped because nothing was
	// configured to perform it. The zero value means executed. It is not a
	// verdict: Status alone decides the gate. It exists so the people and
	// agents reading a run are not told "review passed" about a review nobody
	// performed. OutcomeReason is the short clause explaining a non-executed
	// outcome ("no agent configured", "inputs unchanged").
	//
	// Neither field is part of the signed RunRecord or the evidence chain;
	// they are read only by delivery surfaces.
	Outcome       StepOutcome
	OutcomeReason string
	// Agent is the coding agent the step invoked, set only by a step that
	// actually ran one. Empty for a skipped or cached agent step and for every
	// non-agent step. Like Outcome it is not part of the evidence chain; a
	// profiled run's signed record reads it (see Run.StepOutcomes).
	Agent string
	// AgentBoundary is what warden observed about an agent step's execution
	// boundary under a profile that enforces one (isolation, tree guard,
	// environment); nil otherwise. It is not part of the evidence chain; a
	// profiled run's signed record carries what it observed of an agent that
	// ran (see Run.StepOutcomes and StepObservation).
	AgentBoundary *AgentBoundaryObservation
	// PredictionSavedAt is when this pass was saved as a staged prediction
	// (zero when none was). Like Outcome it is read only by delivery
	// surfaces — the background feedback document derives a prediction's
	// expiry from it — and is not part of the evidence chain.
	PredictionSavedAt time.Time
	// Workspaces is set by a per-workspace step: one entry per workspace it
	// ran for, in path order. The kernel adds one `<step>.workspace` evidence
	// record per entry, so what ran where is inside the run's evidence chain.
	Workspaces []WorkspaceOutcome
}

// StepOutcome classifies whether a passing (or failing) step did its work. See
// StepResult.Outcome.
type StepOutcome string

const (
	// StepExecuted is the ordinary case: the step ran its command or agent.
	StepExecuted StepOutcome = "executed"
	// StepCached means an identical earlier pass was reused; nothing ran now.
	StepCached StepOutcome = "cached"
	// StepSkipped means nothing was configured to perform the step, so it
	// passed without examining the change.
	StepSkipped StepOutcome = "skipped"
)

// ExecutionOutcome normalises Outcome for readers: an unset Outcome is
// StepExecuted, and a result carrying prediction Reuse is StepCached even when
// the step that produced it did not set the field.
func (r StepResult) ExecutionOutcome() StepOutcome {
	switch {
	case r.Outcome != "":
		return r.Outcome
	case r.Reuse != nil:
		return StepCached
	default:
		return StepExecuted
	}
}

// OutcomeNote is the non-executed outcome of one step, as delivery surfaces
// report it.
type OutcomeNote struct {
	Outcome StepOutcome `json:"outcome"`
	Reason  string      `json:"reason,omitempty"`
}

// Note returns the step's outcome note, and false when the step executed (and
// so has nothing to qualify).
func (r StepResult) Note() (OutcomeNote, bool) {
	o := r.ExecutionOutcome()
	if o == StepExecuted {
		return OutcomeNote{}, false
	}
	reason := r.OutcomeReason
	if reason == "" && r.Reuse != nil {
		reason = "matched a staged prediction"
	}
	return OutcomeNote{Outcome: o, Reason: reason}, true
}

// String renders the note as the clause that follows a step name:
// "skipped, no agent configured".
func (n OutcomeNote) String() string {
	if n.Reason == "" {
		return string(n.Outcome)
	}
	return string(n.Outcome) + ", " + n.Reason
}
