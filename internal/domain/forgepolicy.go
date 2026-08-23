package domain

import "strings"

// ForgePolicy decides what a range gate does with a commit the FORGE authored:
// a squash merge, a web edit, a Dependabot or nox remediation commit. warden's
// gate is a client-side pre-push hook, so it was never in such a commit's path
// and no note can exist for it — not because anybody bypassed anything.
//
// The zero value REJECTS, deliberately, and for the same reason ExternalPolicy's
// does (ADR 0003): every deployed gate today means "a warden ran on a machine
// before this was pushed", and widening that silently on upgrade would weaken
// every one of them without anyone choosing to.
type ForgePolicy int

const (
	// ForgeReject fails a forge-authored commit like any other un-noted one —
	// but reports it as ReasonForgeAuthored, so the message names what actually
	// happened. The default.
	ForgeReject ForgePolicy = iota
	// ForgeAccept passes a forge-authored commit that carries a VERIFIED
	// signature from a pinned forge key, recording which key vouched for it.
	//
	// This is the setting that lets Dependabot and squash merges through a
	// required gate. It is a real reduction in what the gate asserts, bounded by
	// what the forge's own key can be made to sign: a developer's --no-verify
	// push is signed with their key or not at all, so it cannot reach this path.
	ForgeAccept
)

// GitHubWebFlowKeys are the fingerprints GitHub signs forge-created commits
// with, published at https://github.com/web-flow.gpg.
//
// FULL fingerprints, never the 64-bit key ids (4AEE18F83AFDEB23,
// B5690EEEBB952194) they end in. A short key id is 64 bits of an attacker's
// choosing given enough compute, and it arrives inside the signature packet —
// which is to say, from the same place as the thing being checked. Matching on
// it would let a commit nominate its own inspector, the failure this codebase
// already refuses when it reads the trusted-signer roster from the base ref
// rather than from the head under inspection.
//
// GitHub-specific on purpose. A self-hosted GitLab or Gitea does not
// necessarily sign the commits it creates, and there is nothing safe to key on
// when it does not; a repository on such a forge should leave ForgeReject in
// place rather than be given a knob that quietly means "trust the committer
// field".
var GitHubWebFlowKeys = []string{
	"5DE3E0509C47EA3CF04A42D34AEE18F83AFDEB23",
	"968479A1AFF927E37D1A566BB5690EEEBB952194",
}

// ForgeKeyMatches reports whether a VERIFIED signature fingerprint is one of the
// pinned forge keys. Comparison is case-insensitive because git and gpg disagree
// about the case of hex fingerprints.
//
// An empty fingerprint never matches. Repo.CommitSignature returns "" whenever
// git could not verify the signature, so "the key could not be checked" cannot
// be mistaken here for "the key is not the forge's" — both fail closed, which is
// the only safe direction, but only one of them means a runner is missing the
// public key and the operator should be told.
func ForgeKeyMatches(fingerprint string, pinned []string) bool {
	fp := strings.ToUpper(strings.TrimSpace(fingerprint))
	if fp == "" {
		return false
	}
	for _, k := range pinned {
		if strings.ToUpper(strings.TrimSpace(k)) == fp {
			return true
		}
	}
	return false
}
