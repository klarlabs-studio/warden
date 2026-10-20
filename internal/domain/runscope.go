package domain

import (
	"fmt"
	"slices"
	"strings"
)

// RunScope records which workspaces a run checked (design 0006 §4). It is
// written only by a repository that configures `workspaces:`, and it is inside
// the signed payload, so it can neither be attached to nor stripped from a
// signed note.
//
// A record WITHOUT a scope is a full run: every note written before monorepo
// mode existed, and every note from a repository without `workspaces:`, stays
// byte-identical and verifies exactly as before.
//
// A scope whose Kind is SelectionAffected says something weaker than a full
// run: "these workspaces were checked on this tree; the others were checked by
// the trusted full run Anchor, and nothing they declare a dependency on has
// changed since". Undeclared reads across workspaces are not detected, so every
// verifier refuses such a record by default (see RunRecord.FullScope).
//
// The struct is deliberately complete for what a writer records, including
// fields no writer fills yet (Sparse). An older warden that meets a field it
// does not know drops it, recomputes a different payload and fails the
// signature, so a field added to this struct later would make every pinned
// verifier of this release refuse every scoped note, full ones included.
type RunScope struct {
	// Version is the selection algorithm version (SelectionVersion) the
	// writer selected by.
	Version int `json:"version"`
	// Kind is SelectionFull or SelectionAffected. Anything else is a kind
	// this warden does not know, and is never read as full.
	Kind SelectionKind `json:"kind"`
	// Anchor is the trusted full-run commit an affected selection was
	// measured from, and AnchorTree its tree. Empty for a full run.
	Anchor     string `json:"anchor,omitempty"`
	AnchorTree string `json:"anchor_tree,omitempty"`
	// HeadTree is the tree the per-workspace checks ran on. A writer refuses
	// to record an affected scope unless it equals the attested commit's tree.
	HeadTree string `json:"head_tree"`
	// ConfigurationDigest is the effective configuration's digest, and
	// GraphDigest the workspace graph's at HeadTree.
	ConfigurationDigest string `json:"configuration_digest"`
	GraphDigest         string `json:"graph_digest"`
	// SelectionDigest is Selection.Digest of the selection: the changed paths,
	// the rules, both graph and configuration digests, and the result.
	SelectionDigest string `json:"selection_digest"`
	// Workspaces lists every discovered workspace path, sorted.
	Workspaces []string `json:"workspaces"`
	// Full names the rule that forced a full selection, when one did.
	Full *ForcedFull `json:"full,omitempty"`
	// Selected lists the selected workspaces with every reason each was
	// selected, sorted by path.
	Selected []SelectedWorkspace `json:"selected,omitempty"`
	// Removed lists affected workspaces the change deleted.
	Removed []string `json:"removed,omitempty"`
	// Steps records, per per-workspace step, the workspaces it ran for and the
	// digest of its command template.
	Steps map[StepName]StepScope `json:"steps,omitempty"`
	// Sparse records the cone of a partial worktree. A sparse run did not
	// check out the whole tree, so its record is never read as full, whatever
	// its Kind says.
	Sparse *SparseScope `json:"sparse,omitempty"`
}

// StepScope is what one per-workspace step ran for.
type StepScope struct {
	// Selected lists the workspace paths the step ran for, sorted.
	Selected []string `json:"selected"`
	// TemplateDigest is the digest of the step's command template.
	TemplateDigest string `json:"template_digest"`
}

// SparseScope is the cone of a sparse worktree: the directories checked out.
type SparseScope struct {
	Cone []string `json:"cone"`
}

// IsFull reports whether the scope attests a full run. A nil scope is full:
// that is every note without one. Otherwise the scope must say SelectionFull
// under a selection algorithm version this warden knows, and must not record
// a sparse cone. Everything else, including a kind or version from a newer
// warden, is not full: the safe reading of a scope this warden cannot
// interpret is "partial".
func (s *RunScope) IsFull() bool {
	if s == nil {
		return true
	}
	return s.Kind == SelectionFull && s.Version >= 1 && s.Version <= SelectionVersion && s.Sparse == nil
}

// Summary renders the scope for one line of human output, such as
// "affected: test[3/12 workspaces]". A nil scope renders as "".
func (s *RunScope) Summary() string {
	if s == nil {
		return ""
	}
	var out string
	switch {
	case s.IsFull():
		out = string(SelectionFull)
	case s.Kind == SelectionAffected || s.Kind == SelectionFull:
		// A full kind that IsFull still refuses (a sparse cone, an unknown
		// version) is named as what this warden reads it as.
		out = string(SelectionAffected)
		if s.Kind == SelectionFull {
			out = "full (read as partial by this warden)"
		}
	default:
		out = fmt.Sprintf("unknown kind %q (read as partial)", s.Kind)
	}
	steps := make([]StepName, 0, len(s.Steps))
	for step := range s.Steps {
		steps = append(steps, step)
	}
	slices.Sort(steps)
	parts := make([]string, 0, len(steps))
	for _, step := range steps {
		parts = append(parts, fmt.Sprintf("%s[%d/%d workspaces]", step, len(s.Steps[step].Selected), len(s.Workspaces)))
	}
	if len(parts) > 0 {
		out += ": " + strings.Join(parts, ", ")
	} else {
		out += fmt.Sprintf(": %d/%d workspaces", len(s.Selected), len(s.Workspaces))
	}
	if s.Sparse != nil {
		out += fmt.Sprintf(", sparse checkout of %d directories", len(s.Sparse.Cone))
	}
	return out
}

// FullScope reports whether the record attests a full run: its own scope is
// full, and so is the scope of the original it carries, when it carries one.
//
// The carried original is checked too even though carriedOriginalHolds already
// requires the two payloads to be equal: that equality is only checked when a
// key is pinned, and a record whose own scope was stripped while it still
// carries an affected original must not read as full under any policy.
func (r RunRecord) FullScope() bool {
	if !r.Scope.IsFull() {
		return false
	}
	if r.CarriedOriginal != nil {
		return r.CarriedOriginal.FullScope()
	}
	return true
}
