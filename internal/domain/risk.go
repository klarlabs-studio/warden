package domain

// Risk is the built-in risk classification (§5.3). v0 has two levels; there is
// no pluggable scorer.
type Risk string

const (
	RiskLow  Risk = "low"
	RiskHigh Risk = "high"
)

// DiffStats summarizes the change under evaluation, computed against the
// merge-base before the pipeline starts.
type DiffStats struct {
	FilesTouched int
	LinesChanged int
	// Paths are the repo-relative paths touched by the diff, used for
	// path-glob rule matching.
	Paths []string
}

// RiskThresholds are the configurable cutoffs for the built-in heuristic.
type RiskThresholds struct {
	DiffLinesHigh    int
	FilesTouchedHigh int
}

// DefaultRiskThresholds mirror the documented defaults in §5.1.
func DefaultRiskThresholds() RiskThresholds {
	return RiskThresholds{DiffLinesHigh: 400, FilesTouchedHigh: 15}
}

// Classify returns RiskHigh if either dimension exceeds its threshold, else
// RiskLow (§5.3).
func (t RiskThresholds) Classify(d DiffStats) Risk {
	if d.LinesChanged > t.DiffLinesHigh || d.FilesTouched > t.FilesTouchedHigh {
		return RiskHigh
	}
	return RiskLow
}
