package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Full rules that only a run applies. Select never returns them: they say why
// a run checked every workspace without asking Select at all.
const (
	// FullRuleSelectAll: the configuration says `workspaces.select: all`
	// (the default).
	FullRuleSelectAll FullRule = "select-all"
	// FullRulePrePush: pre-push runs per-workspace steps for every
	// workspace. Affected selection there needs a full-scope anchor and a
	// scope recorded in the note, neither of which this release writes.
	FullRulePrePush FullRule = "pre-push"
	// FullRuleWriterOrder: a step whose writes are kept (rebase, auto_fix,
	// writes:) runs at or after a per-workspace step, so the tree the
	// selection was computed on is not the tree that gets committed.
	FullRuleWriterOrder FullRule = "writer-order"
)

// FullSelection selects every workspace of head, for the given reason. It is
// the selection a run uses when it does not apply Select: its Kind is
// SelectionFull and every workspace carries ReasonFull, exactly as a full
// result of Select would.
func FullSelection(head WorkspaceGraph, why ForcedFull) (Selection, error) {
	digest, err := head.Digest()
	if err != nil {
		return Selection{}, fmt.Errorf("workspace graph: %w", err)
	}
	sel := Selection{
		Version:         SelectionVersion,
		Kind:            SelectionFull,
		Full:            &why,
		Workspaces:      []SelectedWorkspace{},
		Removed:         []string{},
		HeadGraphDigest: digest,
		Changed:         []string{},
		Rules:           SelectionRules{}.canonical(),
	}
	for _, w := range head.Canonical().Workspaces {
		sel.Workspaces = append(sel.Workspaces, SelectedWorkspace{
			Path: w.Path, Name: w.Name, Reasons: []SelectionReason{{Kind: ReasonFull}},
		})
	}
	return sel, nil
}

// Only returns the part of g whose workspaces belong to one of ecosystems,
// with edges into the rest dropped. An empty list keeps every workspace.
// Discovery reads every ecosystem it knows; a run uses only the ones
// `workspaces.discover` names.
func (g WorkspaceGraph) Only(ecosystems []string) WorkspaceGraph {
	if len(ecosystems) == 0 {
		return g.Canonical()
	}
	kept := map[string]bool{}
	for _, w := range g.Workspaces {
		if slices.Contains(ecosystems, w.Ecosystem) {
			kept[w.Path] = true
		}
	}
	var out []Workspace
	for _, w := range g.Workspaces {
		if !kept[w.Path] {
			continue
		}
		var deps []string
		for _, d := range w.Deps {
			if kept[d] {
				deps = append(deps, d)
			}
		}
		w.Deps = deps
		out = append(out, w)
	}
	return WorkspaceGraph{Workspaces: out}.Canonical()
}

// WorkspaceSelection is what a per-workspace step runs over: the selection a
// run computed for it and the graph it was computed on.
type WorkspaceSelection struct {
	Selection Selection
	// Graph is the head graph the selection covers, limited to the
	// ecosystems `workspaces.discover` names. It supplies each selected
	// workspace's ecosystem.
	Graph WorkspaceGraph
}

// Targets returns the selected workspaces a step runs for, sorted by path:
// those of the given ecosystems, or all selected ones when the list is empty.
func (s WorkspaceSelection) Targets(ecosystems []string) []Workspace {
	byPath := map[string]Workspace{}
	for _, w := range s.Graph.Workspaces {
		byPath[w.Path] = w
	}
	var out []Workspace
	for _, sw := range s.Selection.Workspaces {
		w, ok := byPath[sw.Path]
		if !ok {
			continue
		}
		if len(ecosystems) > 0 && !slices.Contains(ecosystems, w.Ecosystem) {
			continue
		}
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b Workspace) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// Describe renders the selection for a person: "affected" or "full" with the
// rule that forced it, and how many of the discovered workspaces it covers.
func (s WorkspaceSelection) Describe() string {
	n, total := len(s.Selection.Workspaces), len(s.Graph.Workspaces)
	if s.Selection.Kind != SelectionFull || s.Selection.Full == nil {
		return fmt.Sprintf("affected: %d of %d workspace(s)", n, total)
	}
	why := string(s.Selection.Full.Rule)
	if p := s.Selection.Full.Path; p != "" {
		why += " " + p
	}
	return fmt.Sprintf("all %d workspace(s); %s", total, why)
}

// Per-workspace step modes, as WorkspaceOutcome.Mode records them.
const (
	WorkspaceModeEach  = "each"
	WorkspaceModeBatch = "batch"
)

// WorkspaceOutcome is what one workspace's part of a per-workspace step did.
type WorkspaceOutcome struct {
	// Workspace is the workspace's path, its identity.
	Workspace string
	Name      string
	// Status is the verdict of the command that ran for it. For a batch: step
	// one command ran for every selected workspace, so Status is that one
	// command's verdict and says nothing about this workspace alone.
	Status StepStatus
	// Mode is WorkspaceModeEach or WorkspaceModeBatch.
	Mode string
}

// FailedWorkspaces returns the workspaces whose own command failed, in the
// step's order. A batch result names none: its one command failing does not
// say which of its workspaces broke.
func (r StepResult) FailedWorkspaces() []string {
	var out []string
	for _, w := range r.Workspaces {
		if w.Mode == WorkspaceModeEach && w.Status == StepFail {
			out = append(out, w.Workspace)
		}
	}
	return out
}

// WorkspaceExecutionNotice says what per-workspace steps do in hook under
// select mode, for every surface that shows the resolved configuration. It
// states what runs where, and that no workspace scope is recorded anywhere.
func WorkspaceExecutionNotice(hook Hook, mode WorkspaceSelectMode) string {
	switch {
	case hook == PrePush:
		return "per-workspace steps run for every discovered workspace at pre-push (select: affected applies to pre-commit only in this release); each workspace's result is evidence in the note, but the note records no workspace scope yet"
	case mode == WorkspaceSelectAffected:
		return "per-workspace steps run for the workspaces the staged change affects (changed ones and their dependents), and for every workspace when a selection rule forces full or a step that keeps its writes runs at or after one; nothing is signed at pre-commit"
	default:
		return "per-workspace steps run for every discovered workspace (select: all); nothing is signed at pre-commit"
	}
}
