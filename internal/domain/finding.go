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
type Finding struct {
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
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
