package domain

import "testing"

// Attests folds three distinct failures into one boolean, and every caller
// rendered that boolean as "TAMPERED". Two of the three are innocent, and the
// commonest of them — a note left behind by a rebase — is something people do
// deliberately every day.
//
// Naming them separately costs nothing and stops the tool accusing its user of
// something the data does not show. The GATE is unchanged: all three still fail
// Attests, so none of them verify.
func TestRunRecord_AttestDefect_NamesTheActualFailure(t *testing.T) {
	sound := func() RunRecord {
		ev := []EvidenceEntry{{Hash: "h1"}, {Hash: "h2", PreviousHash: "h1"}}
		return RunRecord{CommitSHA: "abc", EvidenceChainRoot: "h1", Evidence: ev}
	}

	t.Run("attests", func(t *testing.T) {
		r := sound()
		if !r.Attests("abc") {
			t.Fatal("fixture must attest, or the cases below prove nothing")
		}
		if got := r.AttestDefect("abc"); got != "" {
			t.Errorf("a record that attests has no defect, got %q", got)
		}
	})

	t.Run("no evidence", func(t *testing.T) {
		r := sound()
		r.Evidence = nil
		r.EvidenceChainRoot = ""
		if got := r.AttestDefect("abc"); got != DefectNoEvidence {
			t.Errorf("defect = %q, want %q", got, DefectNoEvidence)
		}
	})

	t.Run("chain broken is the only one that means tampering", func(t *testing.T) {
		r := sound()
		r.Evidence[1].PreviousHash = "not-h1"
		if got := r.AttestDefect("abc"); got != DefectChainBroken {
			t.Errorf("defect = %q, want %q", got, DefectChainBroken)
		}
	})

	t.Run("unbound is what a rebase leaves behind", func(t *testing.T) {
		r := sound()
		if got := r.AttestDefect("a-different-sha"); got != DefectUnbound {
			t.Errorf("defect = %q, want %q", got, DefectUnbound)
		}
		if r.Attests("a-different-sha") {
			t.Error("an unbound record must still fail the gate — only the LABEL changes")
		}
	})
}

// The defect must reach the report, or the renderer has nothing to say.
func TestNewCommitStatus_CarriesTheDefect(t *testing.T) {
	rec := &RunRecord{
		CommitSHA:         "other",
		EvidenceChainRoot: "h1",
		Evidence:          []EvidenceEntry{{Hash: "h1"}},
	}
	cs := NewCommitStatus("abc", "a", "d", "s", rec)
	if cs.ChainIntact {
		t.Fatal("a note bound to another commit must not read as intact")
	}
	if cs.NoteDefect != DefectUnbound {
		t.Errorf("NoteDefect = %q, want %q", cs.NoteDefect, DefectUnbound)
	}
}

// A commit whose note is sound carries no defect string, so nothing downstream
// has to special-case the empty value.
func TestNewCommitStatus_NoDefectWhenSound(t *testing.T) {
	rec := &RunRecord{
		CommitSHA:         "abc",
		EvidenceChainRoot: "h1",
		Evidence:          []EvidenceEntry{{Hash: "h1"}},
	}
	cs := NewCommitStatus("abc", "a", "d", "s", rec)
	if !cs.ChainIntact || cs.NoteDefect != "" {
		t.Errorf("sound note: ChainIntact=%v NoteDefect=%q", cs.ChainIntact, cs.NoteDefect)
	}
}
