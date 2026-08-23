package domain

// VerifyReason is why a single commit failed a range provenance gate, or the
// empty string when it passed. It is a domain value: the classification is a
// property of the commit-plus-note, independent of how a gate reports it.
type VerifyReason string

const (
	// ReasonOK is the zero value — the commit carries trustworthy provenance to
	// the depth the gate required.
	ReasonOK VerifyReason = ""
	// ReasonMissing: no refs/notes/warden record exists for the commit (a
	// --no-verify push, an uninstalled hook, or a commit made outside warden).
	ReasonMissing VerifyReason = "missing"
	// ReasonBrokenChain: a note exists but does not Attest this commit — its
	// evidence chain is broken, it is empty, or it was transplanted from another
	// commit. This is the tampering case the leaky doctor gate lets through.
	ReasonBrokenChain VerifyReason = "broken-chain"
	// ReasonUnsigned: the gate required a valid signature (--require-signed or
	// --key) but the note is unsigned or its signature does not verify.
	ReasonUnsigned VerifyReason = "unsigned"
	// ReasonUntrusted: the signature verifies but was made by a key outside the
	// pinned trusted set (--key).
	ReasonUntrusted VerifyReason = "untrusted"
	// ReasonUnreadable: a note EXISTS for the commit but could not be decoded —
	// malformed JSON, most often from a hand-edit or from `git notes merge`,
	// whose cat_sort_uniq strategy concatenates two records into one blob.
	//
	// Kept distinct from ReasonMissing because the two need opposite fixes and
	// the wrong one wastes real time: a missing note is recovered by committing
	// through the gate again or by `reattest`, while an unreadable one means the
	// blob must be restored — re-committing will not touch it. Reporting the
	// corrupt case as "missing" sends the reader to `--no-verify` and hook
	// configuration, neither of which is involved. (#195)
	ReasonUnreadable VerifyReason = "unreadable"
	// ReasonForgeAuthored: no note, AND the commit object carries a verified
	// signature from a pinned forge key — the forge created this commit, so no
	// developer machine was ever in its path and no local gate could have run.
	//
	// A FAILING reason by default, and named separately because ReasonMissing
	// tells the reader "pushed with --no-verify, or made outside warden": two
	// developer-bypass causes, both wrong here, and an accusation aimed at
	// whoever opened the Dependabot PR. What a gate does about this commit is a
	// policy question (ForgePolicy); what it CALLS the commit is not.
	ReasonForgeAuthored VerifyReason = "forge-authored"
)

// CommitVerdict is one commit's provenance outcome in a range gate. Reason is
// ReasonOK ("") when the commit passed.
type CommitVerdict struct {
	SHA    string       `json:"sha"`
	Reason VerifyReason `json:"reason,omitempty"`
	// CoveredBy names the commit whose signed push-span vouches for this one,
	// when it carries no note of its own. It records HOW the commit passed —
	// covered by a gated push rather than individually attested — so an auditor
	// reading the gate can tell the two apart instead of seeing an
	// undifferentiated green.
	CoveredBy string `json:"covered_by,omitempty"`
	// ForgeSigner names the pinned forge key whose verified signature let this
	// commit pass without a note, when ForgeAccept is in force. Recorded for the
	// same reason as CoveredBy: HOW a commit passed is part of the verdict. A
	// green range containing forge-accepted commits asserts strictly less than
	// one where every commit was gated, and an auditor must be able to see the
	// difference rather than reading an undifferentiated pass.
	ForgeSigner string `json:"forge_signer,omitempty"`
}

// OK reports whether the commit passed the gate.
func (v CommitVerdict) OK() bool { return v.Reason == ReasonOK }
