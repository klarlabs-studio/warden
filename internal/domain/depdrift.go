package domain

import "fmt"

// DepDrift records that the dependencies a run actually used were not the ones
// the commit's lockfile specifies.
//
// This lives in the record because the record is the durable artifact. Warden
// exposes node_modules from the live checkout rather than reinstalling, so the
// tracked tree comes from the commit and the dependency tree comes from the
// machine. RunRecord.Dependencies digests the lockfiles the COMMIT carries —
// accurate, but silent about whether the run resolved against them. A verifier
// reading a note months later sees digested lockfiles and no hint that the
// steps may have seen something else.
//
// Narrowing that claim in a source comment protects nobody. Putting the drift
// in the record puts it inside the evidence chain and the signature, so the
// attestation says what it actually knows (#204).
type DepDrift struct {
	// Lockfile is the repo-relative path of the committed lockfile.
	Lockfile string `json:"lockfile"`
	// Missing are packages the lockfile requires that are not installed.
	Missing []string `json:"missing,omitempty"`
	// Mismatched are packages installed at a version other than the one the
	// lockfile pins, rendered as "name: installed != locked".
	Mismatched []string `json:"mismatched,omitempty"`
}

// Summary renders the drift as a single human-readable line.
func (d DepDrift) Summary() string {
	switch {
	case len(d.Mismatched) > 0 && len(d.Missing) > 0:
		return fmt.Sprintf("%s: %d package(s) at a different version, %d missing",
			d.Lockfile, len(d.Mismatched), len(d.Missing))
	case len(d.Mismatched) > 0:
		return fmt.Sprintf("%s: %d package(s) installed at a different version than the lockfile pins",
			d.Lockfile, len(d.Mismatched))
	default:
		return fmt.Sprintf("%s: %d package(s) in the lockfile are not installed", d.Lockfile, len(d.Missing))
	}
}

// MaxDriftExamples bounds how many package names a report names outright.
// A drifted install is usually drifted in bulk — after a branch switch every
// changed package differs — and a thousand-line warning is a wall nobody
// reads, which would defeat the point of warning at all.
const MaxDriftExamples = 5

// Examples returns up to MaxDriftExamples entries, most useful first, with a
// trailing "and N more" when the list was truncated.
func (d DepDrift) Examples() []string {
	all := append(append([]string{}, d.Mismatched...), d.Missing...)
	if len(all) <= MaxDriftExamples {
		return all
	}
	out := append([]string{}, all[:MaxDriftExamples]...)
	return append(out, fmt.Sprintf("...and %d more", len(all)-MaxDriftExamples))
}
