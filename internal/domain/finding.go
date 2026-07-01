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
}
