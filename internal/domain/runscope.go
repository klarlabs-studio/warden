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

// Proportion returns how many workspaces the scope selected and how many were
// discovered, for an "affected 3/12" label. A nil scope selects nothing of
// nothing.
func (s *RunScope) Proportion() (selected, total int) {
	if s == nil {
		return 0, 0
	}
	return len(s.Selected), len(s.Workspaces)
}

// Label renders the scope in a few words for a one-line status, such as
// "affected 3/12". It is "full" for a full scope and "" for none, so a
// surface can append it only when there is something to say.
func (s *RunScope) Label() string {
	switch {
	case s == nil:
		return ""
	case s.IsFull():
		return string(SelectionFull)
	}
	n, m := s.Proportion()
	return fmt.Sprintf("%s %d/%d", SelectionAffected, n, m)
}

// SignedByAny reports whether r's signer is one of keys, each given as a full
// public key or a fingerprint in either scheme's encoding (an ed25519
// fingerprint from `warden key show`, or an SSH key or SHA256 fingerprint off
// a forge). It checks identity only: the caller checks the signature verifies.
func (r RunRecord) SignedByAny(keys []string) bool {
	fp := r.SignerFingerprint()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if k == r.PublicKey || k == fp || KeyFingerprint(k) == fp || SSHKeyFingerprint(k) == fp {
			return true
		}
	}
	return false
}

// AnchorRequirement is what a full-scope anchor must match for an affected
// selection to be measured from it (design 0006 §4).
type AnchorRequirement struct {
	// ConfigurationDigest is the effective configuration of the run that
	// wants an anchor. The anchor must have run under the same one.
	ConfigurationDigest string
	// Steps maps every per-workspace step of that run to its template
	// digest. The anchor must have run each of them, from the same template.
	Steps map[StepName]string
	// TrustedKeys are the signers whose full runs may anchor.
	TrustedKeys []string
}

// QualifiesAsAnchor reports whether r, read from the note on commit sha whose
// tree is tree, is a full-scope run an affected selection may rest on:
//
//   - it attests sha (bound to it, evidence chain intact) and is a local run,
//     not an external-run attestation;
//   - it is signed, the signature verifies, and the signer is trusted;
//   - it records a scope, and that scope (with any carried original's) is
//     full: a note without a scope says nothing about which per-workspace
//     steps ran or under which configuration, so it never anchors;
//   - the scope's HeadTree is the commit's tree: its per-workspace checks ran
//     on exactly what the commit holds, not on a tree a later writer changed;
//   - it ran under the same configuration digest, and ran every step the
//     requirement names from the same template.
//
// An affected record never qualifies, so a partial check can only ever rest on
// a full one: the chain is one level deep.
func (r RunRecord) QualifiesAsAnchor(sha, tree string, req AnchorRequirement) bool {
	if r.Scope == nil || !r.FullScope() || r.IsExternal() || !r.Attests(sha) {
		return false
	}
	if len(req.TrustedKeys) == 0 || !r.VerifySignature() || !r.SignedByAny(req.TrustedKeys) {
		return false
	}
	if tree == "" || r.Scope.HeadTree != tree {
		return false
	}
	if req.ConfigurationDigest == "" || r.Scope.ConfigurationDigest != req.ConfigurationDigest {
		return false
	}
	for step, digest := range req.Steps {
		got, ok := r.Scope.Steps[step]
		if !ok || digest == "" || got.TemplateDigest != digest {
			return false
		}
	}
	return true
}

// TemplateDigest is the digest of a per-workspace step's definition: its
// command template, mode and ecosystems. A scope records it per step, so an
// anchor that ran the step from a different template does not anchor it.
func (s PerWorkspaceStep) TemplateDigest() (string, error) {
	return DigestJSON(s)
}

// ScopeInput is what a run knows when it records its scope.
type ScopeInput struct {
	// Selection is the selection the run's per-workspace steps ran over.
	Selection WorkspaceSelection
	// HeadTree is the tree the selection was computed on, which the
	// per-workspace checks ran on.
	HeadTree string
	// Anchor and AnchorTree name the full-scope run an affected selection
	// was measured from. Empty for a full selection.
	Anchor, AnchorTree string
	// ConfigurationDigest is the effective configuration's digest.
	ConfigurationDigest string
	// Steps are the per-workspace steps that ran, with their definitions.
	Steps map[StepName]PerWorkspaceStep
}

// NewRunScope builds the scope a run records. It refuses an affected
// selection without an anchor and a selection whose kind it cannot name: a
// scope is a signed claim, and a malformed one must fail the write rather
// than reach a note.
func NewRunScope(in ScopeInput) (RunScope, error) {
	sel := in.Selection.Selection
	switch sel.Kind {
	case SelectionFull:
		if sel.Full == nil {
			return RunScope{}, fmt.Errorf("a full selection must name the rule that forced it")
		}
	case SelectionAffected:
		if in.Anchor == "" || in.AnchorTree == "" {
			return RunScope{}, fmt.Errorf("an affected selection must name the full-scope anchor it was measured from")
		}
	default:
		return RunScope{}, fmt.Errorf("unknown selection kind %q", sel.Kind)
	}
	if in.HeadTree == "" {
		return RunScope{}, fmt.Errorf("a scope must name the tree its checks ran on")
	}
	digest, err := sel.Digest()
	if err != nil {
		return RunScope{}, fmt.Errorf("selection digest: %w", err)
	}
	graph := in.Selection.Graph.Canonical()
	all := make([]string, 0, len(graph.Workspaces))
	for _, w := range graph.Workspaces {
		all = append(all, w.Path)
	}
	slices.Sort(all)
	scope := RunScope{
		Version:             sel.Version,
		Kind:                sel.Kind,
		HeadTree:            in.HeadTree,
		ConfigurationDigest: in.ConfigurationDigest,
		GraphDigest:         sel.HeadGraphDigest,
		SelectionDigest:     digest,
		Workspaces:          all,
		Full:                sel.Full,
		Selected:            sel.Workspaces,
		Removed:             sel.Removed,
	}
	if sel.Kind == SelectionAffected {
		scope.Anchor, scope.AnchorTree = in.Anchor, in.AnchorTree
	}
	if len(in.Steps) > 0 {
		scope.Steps = make(map[StepName]StepScope, len(in.Steps))
		for name, def := range in.Steps {
			tmpl, err := def.TemplateDigest()
			if err != nil {
				return RunScope{}, fmt.Errorf("template digest of %s: %w", name, err)
			}
			selected := []string{}
			for _, w := range in.Selection.Targets(def.Ecosystems) {
				selected = append(selected, w.Path)
			}
			scope.Steps[name] = StepScope{Selected: selected, TemplateDigest: tmpl}
		}
	}
	return scope, nil
}
