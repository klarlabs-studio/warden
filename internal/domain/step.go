package domain

import (
	"regexp"
	"strings"
)

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
	// StepCredentials refuses a push whose changed files carry a live-looking
	// credential. Named "credentials" rather than "secrets" so it does not
	// shadow the `secrets` command step the gitleaks recipe tells repos to add.
	StepCredentials StepName = "credentials"
	// StepPush is the terminal write-external action the daemon performs
	// itself on a full pass (§4.3). It is never listed in user config; the
	// runner appends it to a passing pre-push run.
	StepPush StepName = "push"
)

// builtinSteps is the set of step names Warden implements natively. Custom
// steps are anything not in this set.
var builtinSteps = map[StepName]bool{
	StepIntent:      true,
	StepRebase:      true,
	StepReview:      true,
	StepTest:        true,
	StepDocument:    true,
	StepLint:        true,
	StepCredentials: true,
	StepPush:        true,
}

// IsBuiltin reports whether s is a Warden built-in step.
func (s StepName) IsBuiltin() bool { return builtinSteps[s] }

// builtinAgentSteps are the built-in steps executed by a coding agent (see
// infrastructure/steps registry). Editing the worktree is part of what these do
// — a document agent writes docs, an intent/review agent may amend — so for
// scheduling they count as tree-writers and never share a parallel batch.
var builtinAgentSteps = map[StepName]bool{
	StepIntent:   true,
	StepReview:   true,
	StepDocument: true,
}

// IsAgentStep reports whether s is a built-in coding-agent step. Custom steps
// assigned an agent by a rule are detected separately via ResolvedPolicy.AgentFor.
func (s StepName) IsAgentStep() bool { return builtinAgentSteps[s] }

// DefaultSteps returns the default step subset for a hook when config omits an
// explicit list: lint only for pre-commit, the full sequence for pre-push.
//
// The pre-push order groups the tree-writing coding-agent steps (review,
// document) ahead of the read-only checks (test, lint) deliberately: agent
// steps run as sequential barriers (they may edit the tree), so keeping the
// checks consecutive lets them share a single parallel batch instead of being
// split by an intervening writer. It also means the checks validate the tree
// after the agents have finished shaping it.
//
// credentials joins the read-only group at pre-push: a leaked token is the one
// failure that cannot be undone by a follow-up commit, so it must be caught
// before anything leaves the machine. It reads only the changed files, so it
// costs nothing measurable and batches with test and lint.
func DefaultSteps(h Hook) []StepName {
	switch h {
	case PreCommit:
		return []StepName{StepLint}
	case PrePush:
		return []StepName{StepIntent, StepRebase, StepReview, StepDocument, StepTest, StepLint, StepCredentials}
	default:
		return nil
	}
}

// DeferredSteps returns the steps in later that ran is missing — the checks a
// split policy postpones to a subsequent hook. It is the domain rule behind
// "lint is green, tests are unrun": a hook that reports a bare pass invites the
// reader to conclude the whole tree is validated, which is only true when
// nothing is deferred. Order follows later; duplicates collapse.
func DeferredSteps(ran, later []StepName) []StepName {
	done := make(map[StepName]bool, len(ran))
	for _, s := range ran {
		done[s] = true
	}
	var out []StepName
	seen := make(map[StepName]bool, len(later))
	for _, s := range later {
		if done[s] || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// JoinSteps renders a step list as a comma-separated string for a one-line
// human-facing summary ("lint, test"). An empty list renders as "".
func JoinSteps(steps []StepName) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}
