package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Agent Trace notarization.
//
// Agent Trace (the Cursor/Cognition RFC) standardizes how AI contributions are
// recorded alongside human ones. Every implementation of it so far is a
// SELF-REPORT: the agent that wrote the code also writes the record saying what
// it wrote. That is useful and it is also unfalsifiable — nothing stops a record
// being edited afterwards, and nothing ties it to a moment when the code was
// actually checked.
//
// Warden is not the authoring tool and should not pretend to be. What it can do
// is NOTARISE: at the moment it gates a commit, hash whatever trace record is
// present and bind that digest into the signed, hash-chained provenance note.
// The trace stays the agent's claim; warden's note turns it into evidence of
// WHEN that claim existed and that it has not changed since.
//
// This deliberately does not validate the record against the spec's schema
// beyond the few fields needed to be sure it IS a trace. Agent Trace is a draft
// RFC and will move; a warden that rejected next quarter's revision would fail
// gates over a spec change, which is a far worse failure than notarizing a
// record it does not fully understand. What is bound is the digest — that
// property holds whatever the schema becomes.

// AgentTraceRef is the notarized reference to an Agent Trace record, as stored
// in the provenance note.
type AgentTraceRef struct {
	// Digest is the SHA-256 of the record's exact bytes, hex-encoded. This is
	// the load-bearing field: it is what makes the trace tamper-evident once the
	// note is signed.
	Digest string `json:"digest"`
	// Path is where the record was read from, relative to the repo root. It
	// locates the record for a later reader; it is not itself a trust anchor.
	Path string `json:"path"`
	// SpecVersion is the record's own declared `version`, recorded verbatim so a
	// reader can tell which revision of a moving spec was notarized without
	// warden having to understand it.
	SpecVersion string `json:"spec_version,omitempty"`
	// TraceID is the record's own `id`, for correlating with whatever system
	// produced it.
	TraceID string `json:"trace_id,omitempty"`
}

// agentTraceDoc is the minimal shape warden reads. The spec has far more in it —
// files, conversations, ranges, contributors — none of which warden interprets.
// Reading only what identifies the record is what keeps this robust to a spec
// that is still changing.
type agentTraceDoc struct {
	Version string            `json:"version"`
	ID      string            `json:"id"`
	Files   []json.RawMessage `json:"files"`
}

// ErrNotAgentTrace reports bytes that are not an Agent Trace record.
var ErrNotAgentTrace = errors.New("not an agent trace record")

// NewAgentTraceRef digests raw and extracts the identifying fields.
//
// It requires version, id and a files array — the spec's required fields, and
// the minimum needed to be confident this is a trace rather than an arbitrary
// JSON file someone happened to point warden at. Notarizing the wrong file
// would put a meaningless digest in a signed record, which is worse than
// notarizing nothing.
func NewAgentTraceRef(path string, raw []byte) (AgentTraceRef, error) {
	var doc agentTraceDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return AgentTraceRef{}, fmt.Errorf("%w: %v", ErrNotAgentTrace, err)
	}
	if doc.Version == "" || doc.ID == "" || doc.Files == nil {
		return AgentTraceRef{}, fmt.Errorf("%w: missing version, id or files", ErrNotAgentTrace)
	}
	sum := sha256.Sum256(raw)
	return AgentTraceRef{
		Digest:      hex.EncodeToString(sum[:]),
		Path:        path,
		SpecVersion: doc.Version,
		TraceID:     doc.ID,
	}, nil
}

// Matches reports whether raw is the exact record this reference notarized.
//
// Byte-exact by design. A trace that has been reformatted is a trace that has
// been rewritten, and warden cannot tell a harmless reformat from a substantive
// edit — so it reports the difference and lets a human decide, rather than
// canonicalising and quietly accepting a record that is no longer the one that
// was signed.
func (a AgentTraceRef) Matches(raw []byte) bool {
	if a.Digest == "" {
		return false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == a.Digest
}
