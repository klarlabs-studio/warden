package domain

// StepName identifies a pipeline step. Built-in steps have reserved names;
// custom steps supplied by a repo author use any other name and run through
// the subprocess adapter.
type StepName string

// Built-in steps. The default pre-push order is the sequence below.
const (
	StepIntent   StepName = "intent"
	StepRebase   StepName = "rebase"
	StepReview   StepName = "review"
	StepTest     StepName = "test"
	StepDocument StepName = "document"
	StepLint     StepName = "lint"
	// StepPush is the terminal write-external action the daemon performs
	// itself on a full pass (§4.3). It is never listed in user config; the
	// runner appends it to a passing pre-push run.
	StepPush StepName = "push"
)

// builtinSteps is the set of step names Warden implements natively. Custom
// steps are anything not in this set.
var builtinSteps = map[StepName]bool{
	StepIntent:   true,
	StepRebase:   true,
	StepReview:   true,
	StepTest:     true,
	StepDocument: true,
	StepLint:     true,
	StepPush:     true,
}

// IsBuiltin reports whether s is a Warden built-in step.
func (s StepName) IsBuiltin() bool { return builtinSteps[s] }

// DefaultSteps returns the default step subset for a hook when config omits an
// explicit list: lint only for pre-commit, the full sequence for pre-push.
func DefaultSteps(h Hook) []StepName {
	switch h {
	case PreCommit:
		return []StepName{StepLint}
	case PrePush:
		return []StepName{StepIntent, StepRebase, StepReview, StepTest, StepDocument, StepLint}
	default:
		return nil
	}
}
