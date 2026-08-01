package domain

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
}

// Reason is the short human explanation of why the step never ran, or "" for
// BlockerNone (where there is nothing to explain — the step ran).
func (b Blocker) Reason() string { return blockerReasons[b] }

// StepResult is the normalized outcome of running one step, whether native or
// subprocess-backed.
type StepResult struct {
	Step     StepName
	Status   StepStatus
	Findings []Finding
	// Fixed reports whether the step mutated the worktree (auto-fix applied).
	Fixed bool
	// Summary is a short human line for TUI/CLI output.
	Summary string
	// Blocker, when set, says the step FAILED WITHOUT RUNNING for an
	// environmental reason. It is meaningful only alongside StepFail.
	Blocker Blocker
}
