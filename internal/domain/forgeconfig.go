package domain

// ForgeConfig opts a repository into accepting commits the FORGE authored.
//
// The problem it solves is structural, not a preference. warden's gate is a
// client-side pre-push hook, so a commit GitHub creates — a squash merge, a web
// edit, a Dependabot or nox remediation commit — was never on a machine where
// warden could run. Under a REQUIRED gate those commits can never pass, so a
// repository either stops merging bot PRs or reaches for the admin override,
// and a check that is routinely overridden is not enforcing anything.
//
// Off by default. Turning it on genuinely lowers what the gate asserts, and
// that has to be somebody's decision rather than an upgrade's side effect.
type ForgeConfig struct {
	// AcceptAuthored lets a commit pass with no warden note when it carries a
	// signature warden could VERIFY against one of Keys.
	AcceptAuthored bool `yaml:"accept_authored"`

	// Keys pins the forge's signing fingerprints. Empty means GitHub's published
	// web-flow keys, which is the only forge warden ships fingerprints for.
	//
	// Full 40-hex fingerprints. Short key ids are refused rather than matched
	// loosely: the id travels inside the signature packet being checked, so
	// trusting it would let the commit nominate its own inspector.
	Keys []string `yaml:"keys"`
}

// ForgeKeys resolves the pinned fingerprints, defaulting to GitHub's.
func (f ForgeConfig) ForgeKeys() []string {
	if len(f.Keys) > 0 {
		return f.Keys
	}
	return GitHubWebFlowKeys
}

// Policy maps the config onto the gate's decision. Reject unless asked.
func (f ForgeConfig) Policy() ForgePolicy {
	if f.AcceptAuthored {
		return ForgeAccept
	}
	return ForgeReject
}
