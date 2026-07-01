package domain

// EvidenceEntry is one hash-chained evidence record as it appears in a git
// note (§9). It mirrors axi-go's domain.EvidenceRecord projected for storage,
// so `warden doctor` can re-verify the chain without importing the kernel.
type EvidenceEntry struct {
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Hash         string `json:"hash"`
	PreviousHash string `json:"previous_hash,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
}

// RunRecord is the payload written to refs/notes/warden for each validated
// commit (§9). It is the tamper-evident provenance a shared branch relies on.
type RunRecord struct {
	RunID             string              `json:"run_id"`
	Timestamp         string              `json:"timestamp"`
	WardenVersion     string              `json:"warden_version"`
	Agent             map[StepName]string `json:"agent"`
	StepsRun          []StepName          `json:"steps_run"`
	MatchedRules      []string            `json:"matched_rules"`
	EvidenceChainRoot string              `json:"evidence_chain_root"`
	Evidence          []EvidenceEntry     `json:"evidence"`
}

// VerifyChain checks the record's evidence links: the recorded root must equal
// the first entry's hash and every entry's PreviousHash must equal the prior
// entry's hash. This detects reordering, truncation, or a rewritten root
// post-push. Full payload recomputation is intentionally out of scope — the
// note stores link hashes, not step payloads, to stay small (§9 tradeoff).
// This is domain logic: what makes a provenance chain intact is a property of
// the record itself, independent of how it was fetched.
func (r RunRecord) VerifyChain() bool {
	if len(r.Evidence) == 0 {
		return r.EvidenceChainRoot == ""
	}
	if r.EvidenceChainRoot != r.Evidence[0].Hash {
		return false
	}
	for i := 1; i < len(r.Evidence); i++ {
		if r.Evidence[i].PreviousHash != r.Evidence[i-1].Hash {
			return false
		}
	}
	return true
}
