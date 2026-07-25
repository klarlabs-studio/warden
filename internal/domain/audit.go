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
	// ReattestableFrom names a validated commit that reproduces this commit's
	// tree exactly, when one exists and this commit has no note of its own — the
	// squash-merge signature. It turns a bare UNVERIFIED into an actionable one:
	// the content WAS gated, under a different commit id, and `warden reattest`
	// can bind that proof to this SHA. Empty when nothing content-identical is
	// validated, which is the genuine "never gated" case.
	ReattestableFrom string
}

// Reattestable reports whether this commit is an un-noted commit whose content
// a validated commit already covers — i.e. the gap is recoverable rather than a
// real hole in the history.
func (c CommitStatus) Reattestable() bool { return !c.HasNote && c.ReattestableFrom != "" }

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
	// "Intact" now requires the note to actually attest THIS commit — an intact
	// but unbound or transplanted note (or a hand-forged `{}`) no longer counts.
	cs.ChainIntact = note.Attests(sha)
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
	for i := range r.Commits {
		if r.Commits[i].HasNote {
			verified++
			if r.Commits[i].ChainIntact {
				intact++
			}
		} else {
			unverified++
		}
	}
	return
}

// Reattestable returns the un-noted commits a validated tree-identical commit
// can vouch for, oldest first — the recoverable share of the unverified count.
func (r AuditReport) Reattestable() []CommitStatus {
	var out []CommitStatus
	for i := range r.Commits {
		if r.Commits[i].Reattestable() {
			out = append(out, r.Commits[i])
		}
	}
	return out
}
