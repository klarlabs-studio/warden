package domain

import "fmt"

// PushForce is how warden pushes a branch whose history no longer fast-forwards
// from the remote — the ordinary result of rebasing onto an updated base.
//
// This is a domain decision, not a git detail, because warden PERFORMS the push
// itself: git's pre-push hook is handed no signal that the developer typed
// --force, so warden has to decide, and the answer is a policy about how much
// history rewriting the repo tolerates.
type PushForce string

const (
	// ForceLease rewrites the remote branch with --force-with-lease pinned to the
	// remote-tracking ref — the value the developer last fetched. A commit pushed
	// by someone else since then invalidates the lease and the push is refused,
	// so this rewrites only history the developer has actually seen.
	ForceLease PushForce = "lease"
	// ForceNever refuses to rewrite, leaving the push to fail as git would.
	ForceNever PushForce = "never"
)

// Valid reports whether f is a force mode warden understands.
func (f PushForce) Valid() bool { return f == ForceLease || f == ForceNever }

// PushConfig is the repo's push policy (`push:` in .warden.yaml).
type PushConfig struct {
	// Force selects the rewrite policy: "lease" (default) or "never".
	Force PushForce `yaml:"force"`
}

// DefaultPushForce is what a repo gets when it says nothing.
//
// Lease is the default deliberately. Warden owns the push, so refusing to
// rewrite does not leave the developer with git's ordinary "use --force"
// nudge — it leaves them with `git push --no-verify`, which skips the gate
// entirely and writes NO provenance. A default that pushes people toward
// bypassing the gate is worse for the thing warden exists to protect than one
// that rewrites a branch they already rewrote locally. The lease keeps the
// property that actually matters: a rewrite can never discard a commit the
// developer has not seen.
const DefaultPushForce = ForceLease

// PushForceMode resolves the effective force policy for this config.
func (c Config) PushForceMode() PushForce {
	if c.Push != nil && c.Push.Force != "" {
		return c.Push.Force
	}
	return DefaultPushForce
}

// validatePush rejects an unknown force mode at load rather than silently
// falling back — a repo that misspells `never` must not discover it by having
// its history rewritten.
func (c Config) validatePush() error {
	if c.Push == nil || c.Push.Force == "" {
		return nil
	}
	if !c.Push.Force.Valid() {
		return fmt.Errorf("invalid push.force %q (want %q or %q)", c.Push.Force, ForceLease, ForceNever)
	}
	return nil
}
