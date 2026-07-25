package domain

import "fmt"

// ScanMode selects what the security-scan gate fails a run on.
type ScanMode string

const (
	// ScanModeDelta fails only on findings the change under review introduced —
	// the fingerprints present at HEAD that were not already present at the
	// merge-base. Findings the tree already carried are reported as a warning
	// with a count, not as a wall. This is the default: a gate that blocks an
	// unrelated one-line change on an inherited backlog gets routed around with
	// `--no-verify`, and a routinely bypassed gate protects nothing while still
	// looking like enforcement.
	ScanModeDelta ScanMode = "delta"
	// ScanModeTotal fails on any unwaived finding in the tree, whoever
	// introduced it. Opt in for a repo that has genuinely reached zero and
	// wants to stay there.
	ScanModeTotal ScanMode = "total"
)

// SecurityScanConfig tunes the security-scan gate (`security_scan:` in
// .warden.yaml). Every field is optional; the zero value is the documented
// default (delta gating, scanner version check on, base ref auto-detected).
type SecurityScanConfig struct {
	// Mode is "delta" (default) or "total". See ScanMode.
	Mode ScanMode `yaml:"mode"`

	// Base overrides the ref the delta is measured against. Empty means the
	// merge-base of HEAD and the branch's upstream, falling back to the remote
	// default branch — i.e. "what this change actually adds".
	Base string `yaml:"base"`

	// VersionCheck toggles the scanner version-drift refusal: warden declines to
	// scan when the scanner on PATH is a different version from the one CI pins,
	// because a scanner that renumbers its rule IDs between releases invalidates
	// every baseline fingerprint at once — the whole triaged corpus reads as
	// net-new. Unset (nil) means enabled; set `version_check: false` to silence
	// it. The check is a no-op when no pin can be discovered.
	VersionCheck *bool `yaml:"version_check"`

	// PinFile names the workflow file that carries the scanner version pin, so
	// warden reads the pin from the one place that already defines it instead of
	// the repo restating it here (a second copy is a second thing to forget).
	// Empty means "search .github/workflows/*.yml".
	PinFile string `yaml:"pin_file"`
}

// ResolvedMode returns the effective mode, substituting the delta default for
// an unset value.
func (s SecurityScanConfig) ResolvedMode() ScanMode {
	if s.Mode == ScanModeTotal {
		return ScanModeTotal
	}
	return ScanModeDelta
}

// VersionCheckEnabled reports whether the scanner version-drift refusal is on.
func (s SecurityScanConfig) VersionCheckEnabled() bool {
	return s.VersionCheck == nil || *s.VersionCheck
}

// IsZero reports whether nothing was configured, so config overlay can tell
// "unset" from "explicitly set to the default".
func (s SecurityScanConfig) IsZero() bool {
	return s.Mode == "" && s.Base == "" && s.VersionCheck == nil && s.PinFile == ""
}

// Validate rejects a mode that is neither of the two documented values. A typo
// ("dela") must not silently resolve to a default: the whole point of the
// setting is choosing how strict the gate is, and quietly picking for the
// operator is how a `total` repo ends up gating on a delta without anyone
// noticing.
func (s SecurityScanConfig) Validate() error {
	switch s.Mode {
	case "", ScanModeDelta, ScanModeTotal:
		return nil
	default:
		return fmt.Errorf("invalid security_scan.mode %q: must be %q or %q", string(s.Mode), string(ScanModeDelta), string(ScanModeTotal))
	}
}

// MergeSecurityScan overlays a child security_scan block onto a base one field
// by field, so a child that sets only `mode:` keeps the base's pin_file. The
// one asymmetry is Mode: `total` is strictly stricter than `delta`, and a
// shared org base config that demands total must not be weakened by a repo
// quietly writing `mode: delta` — the same "a child may add strictness, never
// drop it" rule the trusted-key roster and the writes list already follow.
// VersionCheck is deliberately not held that way: it is a diagnostic, not a
// severity gate, and a repo whose pin lives somewhere warden cannot read needs
// to be able to turn it off.
func MergeSecurityScan(base, child SecurityScanConfig) SecurityScanConfig {
	out := base
	if child.Mode != "" && !weakensMode(base.Mode, child.Mode) {
		out.Mode = child.Mode
	}
	if child.Base != "" {
		out.Base = child.Base
	}
	if child.VersionCheck != nil {
		out.VersionCheck = child.VersionCheck
	}
	if child.PinFile != "" {
		out.PinFile = child.PinFile
	}
	return out
}

// weakensMode reports whether swapping base for child loosens the gate.
func weakensMode(base, child ScanMode) bool {
	return base == ScanModeTotal && child == ScanModeDelta
}
