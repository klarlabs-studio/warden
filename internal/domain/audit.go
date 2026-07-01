package domain

// CommitStatus is one commit's provenance line in an audit (§9). It is a domain
// value object: the classification (verified/intact/unverified) is a property
// of the commit-plus-note, computed by the domain, not the delivery layer.
type CommitStatus struct {
	SHA     string
	Author  string
	Date    string
	Subject string
	// HasNote is true when a refs/notes/warden record exists for the commit.
	HasNote bool
	// ChainIntact reports whether the note's evidence chain verified.
	ChainIntact bool
	RunID       string
	Steps       []StepName
}

// NewCommitStatus classifies a commit from its metadata and optional note.
// A nil note means no provenance exists — the commit is unverified (a
// --no-verify push or one made outside Warden).
func NewCommitStatus(sha, author, date, subject string, note *RunRecord) CommitStatus {
	cs := CommitStatus{SHA: sha, Author: author, Date: date, Subject: subject}
	if note == nil {
		return cs
	}
	cs.HasNote = true
	cs.RunID = note.RunID
	cs.Steps = note.StepsRun
	cs.ChainIntact = note.VerifyChain()
	return cs
}

// AuditReport summarizes provenance for a branch since adoption (§9).
type AuditReport struct {
	Adoption string
	Branch   string
	Commits  []CommitStatus
}

// Counts tallies verified/intact/unverified commits for the summary line.
func (r AuditReport) Counts() (verified, intact, unverified int) {
	for _, c := range r.Commits {
		if c.HasNote {
			verified++
			if c.ChainIntact {
				intact++
			}
		} else {
			unverified++
		}
	}
	return
}
