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
	// NoteDefect names WHY a note failed to attest its commit, when one did.
	// Empty when ChainIntact. See RunRecord.AttestDefect: "TAMPERED" was being
	// printed for three different failures, two of them innocent.
	NoteDefect string `json:"note_defect,omitempty"`
	RunID      string
	Steps      []StepName
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
	// NoRemoteRef marks a gap on a branch with no remote-tracking ref — nothing
	// on it has ever been pushed.
	//
	// warden's provenance note is written by the PRE-PUSH gate. A branch that was
	// never pushed never reached that gate, so its commits carry no note for a
	// reason that has nothing to do with anyone routing around anything. Calling
	// them bypasses accuses someone of evading a gate that was never reachable.
	//
	// Same shape as PreSpanProvenance, one step earlier: the absence of a note is
	// evidence of a bypass only once warden had both the ABILITY and the
	// OPPORTUNITY to write one.
	NoRemoteRef bool `json:"no_remote_ref,omitempty"`
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
func (c CommitStatus) Reattestable() bool {
	return !c.HasNote && !c.Covered() && c.ReattestableFrom != ""
}

// Commit states.
//
// The predicates below PARTITION every commit: exactly one of HasNote, Covered,
// Reattestable, Unpushed, Unattributable and Bypassed holds. That is not
// decoration — the fleet rollup sums the buckets, so an overlap double-counts
// and a hole loses a commit entirely. TestCommitStates_Partition enforces it
// across every combination of the underlying fields, which is what made it safe
// to add Unpushed as a sixth state: the test failed until it was made exclusive,
// exactly as its comment promised.
//
// Ordered strongest-claim-first. Unpushed outranks Unattributable because
// "never pushed" is a definite fact about the branch, while pre-span provenance
// is an ambiguity; a definite explanation beats an ambiguous one.

// Covered reports whether a gated push span published this commit. Such a commit
// has no note of its own and needs none: the span is the claim warden actually
// makes about a multi-commit push.
func (c CommitStatus) Covered() bool { return !c.HasNote && c.CoveredBy != "" }

// Unpushed reports whether this commit sits on a branch that was never pushed,
// so the pre-push gate that writes the note never had an opportunity to run.
// See NoRemoteRef.
func (c CommitStatus) Unpushed() bool {
	return !c.HasNote && !c.Covered() && !c.Reattestable() && c.NoRemoteRef
}

// Unattributable reports whether this commit's status cannot be determined from
// what was recorded, because the provenance around it predates push spans.
// See PreSpanProvenance.
func (c CommitStatus) Unattributable() bool {
	return !c.HasNote && !c.Covered() && !c.Reattestable() && !c.Unpushed() && c.PreSpanProvenance
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
// once warden was capable of writing one — and had the chance.
func (c CommitStatus) Bypassed() bool {
	return !c.HasNote && !c.Covered() && !c.Reattestable() && !c.Unpushed() && !c.PreSpanProvenance
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
	if !cs.ChainIntact {
		cs.NoteDefect = note.AttestDefect(sha)
	}
	return cs
}

// AuditReport summarizes provenance for a branch since adoption (§9).
type AuditReport struct {
	Adoption string
	Branch   string
	// NeverPushed is true when the branch has no remote-tracking ref, so nothing
	// on it has ever reached the pre-push gate. Reported at the branch level as
	// well as per-commit because it is a fact about the branch, and a reader
	// seeing every commit marked unpushed deserves the one-line reason.
	NeverPushed bool
	Commits     []CommitStatus
}

// Tally is an audit's commit counts, one bucket per state the commit-state
// partition distinguishes.
//
// Verified, Covered, Unverified and Unknown account for every commit exactly
// once — Total is their sum. Defective is the one deliberate SUBSET: it names
// why a commit is unverified, so it is counted inside Unverified rather than
// beside it.
type Tally struct {
	// Verified: carries a note that ATTESTS the commit.
	Verified int
	// Covered: no note of its own, but a gated push span published it. warden
	// validates one tree per run and vouches for the span, so this is the normal
	// state of every commit below the tip of a multi-commit push — not a gap.
	Covered int
	// Defective: carries a note that does NOT attest it — unbound, chain broken,
	// or empty. A subset of Unverified.
	Defective int
	// Unverified: warden had both the ability and the opportunity to vouch for
	// this commit and did not — a defective note, a squash-merge binding gap, or
	// a genuine bypass. This is the number an exit code should gate on.
	Unverified int
	// Unknown: warden makes no claim, because it was never in a position to.
	// A branch that was never pushed never reached the gate that writes notes;
	// provenance older than push spans cannot say whether a gap was an
	// intermediate commit or a bypass. Calling either unverified asserts more
	// than the data supports.
	Unknown int
}

// Total is the number of commits tallied.
func (t Tally) Total() int { return t.Verified + t.Covered + t.Unverified + t.Unknown }

// Counts tallies commits by whether their provenance actually stands up.
//
// It switches on the SAME predicates as the commit-state partition above rather
// than re-deriving from HasNote, which is what let the two disagree. Counts used
// to bucket every note-less commit as unverified, so a span-covered commit —
// which doctor prints "✓ (covered by the gated push …)" — was simultaneously
// reported as unverified in the summary line and gated on by doctor's exit code.
// An ordinary three-commit PR (commit, commit, commit, push) therefore printed
// all ✓ and exited 1. The same held for a branch that was never pushed, one line
// after doctor prints "These are not bypasses".
//
// Unverified still deliberately INCLUDES defective, so it stays "warden should
// have been able to vouch for this and cannot": a repo whose notes no longer
// attest anything is precisely a repo that should be flagged.
func (r AuditReport) Counts() Tally {
	var t Tally
	for i := range r.Commits {
		c := &r.Commits[i]
		switch {
		case c.HasNote && c.ChainIntact:
			t.Verified++
		case c.HasNote:
			t.Defective++
			t.Unverified++
		case c.Covered():
			t.Covered++
		case c.Unpushed(), c.Unattributable():
			t.Unknown++
		default: // Reattestable or Bypassed: a real gap either way
			t.Unverified++
		}
	}
	return t
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
