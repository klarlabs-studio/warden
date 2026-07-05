package domain

import "regexp"

// StepName identifies a pipeline step. Built-in steps have reserved names;
// custom steps supplied by a repo author use any other name and run through
// the subprocess adapter.
type StepName string

// stepNameRe is the allowlist for a syntactically safe step name: an
// alphanumeric start followed by alphanumerics, '-' or '_'. It deliberately
// excludes path separators, '.', whitespace and shell metacharacters.
var stepNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Valid reports whether s is a syntactically safe step name. This is the
// security allowlist that keeps a repo-authored custom step name from smuggling
// a path separator or shell metacharacter into
// exec.LookPath("warden-step-"+name): a name like "x/evil" or "../../bin/sh"
// would otherwise be treated by LookPath as a relative path and execute a
// repo-committed binary instead of resolving a trusted step off PATH. All
// built-in step names satisfy this pattern.
func (s StepName) Valid() bool {
	return stepNameRe.MatchString(string(s))
}

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
