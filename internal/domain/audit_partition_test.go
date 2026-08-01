package domain

import "testing"

// Every commit must land in EXACTLY ONE state.
//
// This is the guard the previous three state additions did not have. Covered,
// Reattestable, Unattributable and Bypassed were each added separately, as an
// independent predicate, and nothing forced them to be mutually exclusive. They
// were not: a commit both span-covered and tree-identical to a validated one
// satisfied two at once, so the fleet rollup — which sums the buckets — counted
// it twice.
//
// Exhaustive over every combination of the underlying fields rather than over a
// handful of hand-picked cases, because the combination that broke was one
// nobody would think to write down. The cost of exhaustiveness here is 16 cases.
//
// It is also what makes a FIFTH state safe to add: this fails until the new one
// is made exclusive, instead of silently skewing a number someone acts on. That
// promise was cashed when Unpushed became the sixth — this test failed on it
// before it was ordered against the others, which is exactly the intent.
func TestCommitStates_Partition(t *testing.T) {
	for _, hasNote := range []bool{true, false} {
		for _, coveredBy := range []string{"", "tip99"} {
			for _, reattestable := range []string{"", "pre99"} {
				for _, preSpan := range []bool{true, false} {
					for _, noRemote := range []bool{true, false} {
						c := CommitStatus{
							SHA:               "abc",
							HasNote:           hasNote,
							CoveredBy:         coveredBy,
							ReattestableFrom:  reattestable,
							PreSpanProvenance: preSpan,
							NoRemoteRef:       noRemote,
						}
						states := map[string]bool{
							"noted":          c.HasNote,
							"covered":        c.Covered(),
							"reattestable":   c.Reattestable(),
							"unpushed":       c.Unpushed(),
							"unattributable": c.Unattributable(),
							"bypassed":       c.Bypassed(),
						}
						var held []string
						for name, on := range states {
							if on {
								held = append(held, name)
							}
						}
						if len(held) != 1 {
							t.Errorf("HasNote=%v CoveredBy=%q ReattestableFrom=%q PreSpan=%v NoRemoteRef=%v → states %v (want exactly one)",
								hasNote, coveredBy, reattestable, preSpan, noRemote, held)
						}
					}
				}
			}
		}
	}
}

// A branch that was never pushed cannot have bypassed a PUSH gate. This is the
// proctor case: a local-only repo, renamed away weeks earlier, reporting 61 of
// 61 commits as bypasses — 82% of an entire fleet's bypass count, every one of
// them an accusation warden had no evidence for.
func TestCommitStatus_NeverPushedIsNotBypassed(t *testing.T) {
	c := CommitStatus{SHA: "a", NoRemoteRef: true}
	if !c.Unpushed() {
		t.Error("a gap on a branch with no remote ref must be unpushed")
	}
	if c.Bypassed() {
		t.Error("…and must NOT be a bypass: the pre-push gate never had an opportunity to run")
	}

	pushed := CommitStatus{SHA: "b"}
	if pushed.Unpushed() {
		t.Error("a gap on a pushed branch is not unpushed")
	}
	if !pushed.Bypassed() {
		t.Error("…it is a bypass: the gate was reachable and left no note")
	}
}

// Positive findings still outrank it — a commit warden can vouch for is vouched
// for whether or not the branch was ever pushed.
func TestCommitStatus_UnpushedYieldsToPositiveFindings(t *testing.T) {
	covered := CommitStatus{SHA: "a", CoveredBy: "tip", NoRemoteRef: true}
	if covered.Unpushed() || covered.Bypassed() {
		t.Errorf("a span-covered commit is covered, not unpushed: %+v", covered)
	}
	reattestable := CommitStatus{SHA: "b", ReattestableFrom: "pre", NoRemoteRef: true}
	if reattestable.Unpushed() || reattestable.Bypassed() {
		t.Errorf("a reattestable commit is recoverable, not unpushed: %+v", reattestable)
	}
}

// The precedence when several signals are present, stated as its own assertion
// so a future reordering has to argue with it. A stronger claim wins: a note of
// one's own beats a span, a span beats a recoverable tree, and anything beats
// the ambiguity of a pre-span era.
func TestCommitStates_PrecedenceIsStrongestClaimFirst(t *testing.T) {
	all := CommitStatus{
		HasNote:           true,
		CoveredBy:         "tip",
		ReattestableFrom:  "pre",
		PreSpanProvenance: true,
	}
	if all.Covered() || all.Reattestable() || all.Unattributable() || all.Bypassed() {
		t.Errorf("its own note is the strongest claim and must win outright: %+v", all)
	}

	noNote := all
	noNote.HasNote = false
	if !noNote.Covered() {
		t.Error("a gated push span outranks a recoverable tree and the pre-span era")
	}

	noSpan := noNote
	noSpan.CoveredBy = ""
	if !noSpan.Reattestable() {
		t.Error("a tree-identical validated commit outranks the pre-span era")
	}

	noSource := noSpan
	noSource.ReattestableFrom = ""
	if !noSource.Unattributable() {
		t.Error("with nothing else, pre-span provenance means unattributable")
	}
}
