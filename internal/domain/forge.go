package domain

// PRConfig enables and configures pull-request creation after a passing
// pre-push run (§4.3 step 3). Off by default: warden pushes with provenance
// regardless; opening a PR is an opt-in convenience.
type PRConfig struct {
	Enabled bool `yaml:"enabled"`
	// Base is the branch a PR targets; empty means the forge's default branch.
	Base string `yaml:"base"`
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
