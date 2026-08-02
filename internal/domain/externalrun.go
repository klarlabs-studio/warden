package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ErrExternalRunInvalid is returned when an external-run reference cannot be
// used as evidence. See ExternalRunRef.Validate.
var ErrExternalRunInvalid = errors.New("external run reference is not usable as evidence")

// ExternalRunRef names the platform run that executed the checks, when warden
// did not execute them itself (ADR 0003).
//
// A record carrying one asserts something WEAKER than an ordinary note: "the
// signer vouches that run X on platform P reported these checks passing for
// this commit" — not "warden executed these checks". The two must stay
// distinguishable at verify time, because a consumer enforcing a gate is
// entitled to decide which it accepts. `verify` therefore defaults to refusing
// external attestations; see service.VerifyPolicy.
//
// It follows the shape of RunRecord.ReattestedFrom, which exists for the same
// reason along a different axis: to make a derived claim transparent rather than
// pass it off as a fresh validation.
type ExternalRunRef struct {
	// Provider identifies the platform, e.g. "github-actions". It is part of the
	// claim: "run 42" means nothing without knowing whose run 42.
	Provider string `json:"provider"`
	// RunID and Attempt identify the run within the provider.
	RunID   string `json:"run_id"`
	Attempt int    `json:"attempt,omitempty"`
	// URL is where a human can go to read the run. Informational.
	URL string `json:"url,omitempty"`
	// Repository is the provider's name for the repo, e.g. "owner/name". A run id
	// is only unique within a repository.
	Repository string `json:"repository"`
	// Commit is the SHA the external run executed against.
	//
	// It MUST equal the record's CommitSHA. A run against a different tree proves
	// nothing about this one, and accepting a mismatch is exactly how "CI passed"
	// degrades into "some CI passed, somewhere" — the failure this whole design
	// exists to avoid. Checked by BoundTo, called from RunRecord.Validate.
	Commit string `json:"commit"`
	// Checks names what the external run reported passing, so the note is
	// specific about what was covered rather than asserting a bare "CI passed".
	// A reference with no checks vouches for nothing and is rejected.
	Checks []string `json:"checks"`
}

// Validate reports whether the reference is structurally usable as evidence.
//
// Every field it requires is one without which the claim cannot be checked by
// anybody: a provider and run id to find the run, a repository to disambiguate
// it, a commit to bind it, and at least one check so the note says what was
// actually covered.
func (e ExternalRunRef) Validate() error {
	switch {
	case strings.TrimSpace(e.Provider) == "":
		return fmt.Errorf("%w: no provider", ErrExternalRunInvalid)
	case strings.TrimSpace(e.RunID) == "":
		return fmt.Errorf("%w: no run id", ErrExternalRunInvalid)
	case strings.TrimSpace(e.Repository) == "":
		return fmt.Errorf("%w: no repository", ErrExternalRunInvalid)
	case strings.TrimSpace(e.Commit) == "":
		return fmt.Errorf("%w: no commit", ErrExternalRunInvalid)
	case len(e.Checks) == 0:
		// A reference that names no checks asserts that a run happened, not that
		// anything passed. Writing it would produce a note that reads as an
		// attestation while vouching for nothing.
		return fmt.Errorf("%w: no checks recorded", ErrExternalRunInvalid)
	}
	return nil
}

// BoundTo reports whether the external run executed against sha.
//
// This is the invariant that keeps the claim about THIS commit. Both sides are
// compared in full: a prefix match would let a run against any commit sharing a
// short prefix stand in for this one.
func (e ExternalRunRef) BoundTo(sha string) bool {
	return e.Commit != "" && e.Commit == sha
}

// IsExternal reports whether this record attests an external run rather than a
// local execution. Callers enforcing a gate use it to decide acceptance; see
// service.VerifyPolicy.
func (r RunRecord) IsExternal() bool { return r.ExternalRun != nil }

// ValidateExternal checks the invariants an external-run record must satisfy
// before it is written. It is deliberately separate from Attests, which asks
// whether a record is self-consistent and bound — a question with the same
// answer for local and external records.
//
// Two rules, both load-bearing:
//
//   - the reference must be structurally usable AND describe THIS commit, or the
//     note vouches for a run against some other tree;
//   - the record must be SIGNED. An unsigned external note reaches a consumer
//     with no pinned signer as a bare "checks passed", indistinguishable from a
//     local attestation — the one downgrade the payload mechanism does not
//     already close (ADR 0003).
func (r RunRecord) ValidateExternal() error {
	if r.ExternalRun == nil {
		return nil
	}
	if err := r.ExternalRun.Validate(); err != nil {
		return err
	}
	if !r.ExternalRun.BoundTo(r.CommitSHA) {
		return fmt.Errorf("%w: run executed against %s but the record attests %s",
			ErrExternalRunInvalid, shortSHA(r.ExternalRun.Commit), shortSHA(r.CommitSHA))
	}
	if !r.Signed() {
		return fmt.Errorf("%w: an external attestation must be signed, or a verifier "+
			"with no pinned signer cannot tell it from a local one", ErrExternalRunInvalid)
	}
	return nil
}

// shortSHA abbreviates for messages without truncating something that is not a
// SHA at all.
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(none)"
	}
	return s
}
