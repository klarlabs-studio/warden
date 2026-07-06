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
)

// CommitVerdict is one commit's provenance outcome in a range gate. Reason is
// ReasonOK ("") when the commit passed.
type CommitVerdict struct {
	SHA    string       `json:"sha"`
	Reason VerifyReason `json:"reason,omitempty"`
}

// OK reports whether the commit passed the gate.
func (v CommitVerdict) OK() bool { return v.Reason == ReasonOK }
