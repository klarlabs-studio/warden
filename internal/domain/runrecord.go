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
