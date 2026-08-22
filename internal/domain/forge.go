package domain

// PRConfig enables and configures pull-request creation after a passing
// pre-push run (§4.3 step 3). Off by default: warden pushes with provenance
// regardless; opening a PR is an opt-in convenience.
type PRConfig struct {
	Enabled bool `yaml:"enabled"`
	// Base is the branch a PR targets; empty means the forge's default branch.
	Base string `yaml:"base"`
	// Comment toggles posting a gate-result summary comment on the PR after a
	// passing push. Unset (nil) defaults to enabled when PRs are enabled.
	Comment *bool `yaml:"comment"`
}

// CommentEnabled reports whether to post the gate-result PR comment: on by
// default whenever PR creation is enabled, unless explicitly disabled.
func (c PRConfig) CommentEnabled() bool {
	return c.Enabled && (c.Comment == nil || *c.Comment)
}

// PRInfo identifies a pull request the forge opened or found.
type PRInfo struct {
	URL     string
	Number  int
	Created bool // true when this run opened it, false when it already existed
}

// CIState is the aggregate CI status for a branch's checks.
type CIState string

const (
	CINone    CIState = "none"    // no checks reported
	CIPending CIState = "pending" // checks still running
	CIPassing CIState = "passing" // all checks passed
	CIFailing CIState = "failing" // at least one check failed
)

// CIStatus summarizes a branch's CI checks.
type CIStatus struct {
	State   CIState
	Total   int
	Passed  int
	Failed  int
	Pending int
}

// DefaultStatusContext is the commit-status context warden publishes under.
//
// Deliberately NOT the warden-verify Action's job name ("Warden provenance"),
// which was the first attempt and does not work. GitHub keeps a commit status
// and a check run as separate entries even when they share a name, and a
// required check is satisfied only when every entry under that name passes. So
// publishing under the Action's name adds a green status beside the Action's
// red check run and the branch stays blocked — measured, not assumed.
//
// A context warden alone writes is therefore the only one it can actually
// satisfy. Requiring it is an explicit protection change, which is honest: a
// repository is choosing to accept a locally-produced verdict.
const DefaultStatusContext = "warden/gate"

// StatusConfig controls publishing the gate verdict to the forge as a commit
// status.
//
// Off by default, and deliberately so: publishing writes to a shared, external
// surface that other people read as CI, and a gate should not start doing that
// because somebody upgraded warden.
//
// It exists for repositories whose Actions cannot run — a spending limit, a
// self-hosted-only fleet, an air-gapped mirror. The gate has already produced a
// signed verdict on this machine; this is only the part that tells GitHub. It
// is not a second opinion, and it claims nothing the note does not already say.
type StatusConfig struct {
	// Enabled turns on publishing after a passing, pushed gate run.
	Enabled bool `yaml:"enabled"`

	// Context overrides the status context. Empty means DefaultStatusContext.
	Context string `yaml:"context"`
}

// StatusContext is the context to publish under, defaulted.
func (c StatusConfig) StatusContext() string {
	if c.Context == "" {
		return DefaultStatusContext
	}
	return c.Context
}
