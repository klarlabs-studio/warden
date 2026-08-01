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
// is made exclusive, instead of silently skewing a number someone acts on.
func TestCommitStates_Partition(t *testing.T) {
	for _, hasNote := range []bool{true, false} {
		for _, coveredBy := range []string{"", "tip99"} {
			for _, reattestable := range []string{"", "pre99"} {
				for _, preSpan := range []bool{true, false} {
					c := CommitStatus{
						SHA:               "abc",
						HasNote:           hasNote,
						CoveredBy:         coveredBy,
						ReattestableFrom:  reattestable,
						PreSpanProvenance: preSpan,
					}
					states := map[string]bool{
						"noted":          c.HasNote,
						"covered":        c.Covered(),
						"reattestable":   c.Reattestable(),
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
						t.Errorf("HasNote=%v CoveredBy=%q ReattestableFrom=%q PreSpan=%v → states %v (want exactly one)",
							hasNote, coveredBy, reattestable, preSpan, held)
					}
				}
			}
		}
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
