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
	// CoveredBy names a validated commit whose signed push span published this
	// one — everything in (CoversFrom, tip] went out together, gated as a unit.
	//
	// A run validates ONE tree, the tip's, so warden deliberately does not attest
	// each commit of a multi-commit push individually: that would assert checks
	// which never ran against those trees. What it does assert is the span. A
	// commit inside a trusted span was published by a gated push and is NOT a
	// bypass, even though it carries no note of its own.
	CoveredBy string `json:"covered_by,omitempty"`
	// PreSpanProvenance marks a gap whose surrounding provenance was written by a
	// warden too old to record a push span.
	//
	// warden validates one tree per run, so the intermediate commits of a
	// multi-commit push get no note — the span is what vouches for them. Spans
	// arrived in v0.19.0. A gap next to a note written before that could be an
	// intermediate commit of a gated push OR a real bypass, and NOTHING
	// distinguishes them: the information was never written down.
	//
	// Reporting such a commit as bypassed asserts more than the data supports.
	// Reporting it as verified asserts more still. It is unattributable, and
	// saying so is the only honest reading.
	PreSpanProvenance bool `json:"pre_span_provenance,omitempty"`
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

// Covered reports whether a gated push span published this commit. Such a commit
// has no note of its own and needs none: the span is the claim warden actually
// makes about a multi-commit push.
func (c CommitStatus) Covered() bool { return !c.HasNote && c.CoveredBy != "" }

// Unattributable reports whether this commit's status cannot be determined from
// what was recorded, because the provenance around it predates push spans.
// See PreSpanProvenance.
func (c CommitStatus) Unattributable() bool {
	return !c.HasNote && !c.Covered() && !c.Reattestable() && c.PreSpanProvenance
}

// Bypassed reports whether this commit demonstrably went round the gate: no
// note, no covering push span, no tree-identical validated commit to recover
// from, and provenance recent enough that a span WOULD have been recorded had
// the commit been part of a gated push.
//
// This is the number that should trigger an intervention, so it excludes every
// case that looks like a gap and is not. Counting any of them inflates it, and a
// metric that overstates the problem gets dismissed as noisy — which costs more
// than not having one. The last exclusion is the subtlest: a gap can only be
// called a bypass if the absence of a span is EVIDENCE, and it is only evidence
// once warden was capable of writing one.
func (c CommitStatus) Bypassed() bool {
	return !c.HasNote && !c.Covered() && !c.Reattestable() && !c.PreSpanProvenance
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
