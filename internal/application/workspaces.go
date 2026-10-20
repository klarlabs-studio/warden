package application

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"go.klarlabs.de/warden/internal/domain"
)

// workspaceSelections holds the selection each per-workspace step of the
// current batch runs over. The runner fills it before a batch starts; steps
// read it, possibly concurrently, through StepContext.Selection.
type workspaceSelections struct {
	mu sync.Mutex
	m  map[domain.StepName]*domain.WorkspaceSelection
}

func newWorkspaceSelections() *workspaceSelections {
	return &workspaceSelections{m: map[domain.StepName]*domain.WorkspaceSelection{}}
}

// get implements StepContext.Selection.
func (w *workspaceSelections) get(step domain.StepName) (*domain.WorkspaceSelection, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sel, ok := w.m[step]
	if !ok {
		return nil, fmt.Errorf("no workspace selection was computed for step %s", step)
	}
	return sel, nil
}

func (w *workspaceSelections) set(step domain.StepName, sel *domain.WorkspaceSelection) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[step] = sel
}

// perWorkspaceSteps returns the steps of batch defined under per_workspace:.
func perWorkspaceSteps(resolved domain.ResolvedPolicy, batch []domain.StepName) []domain.StepName {
	ws := resolved.Workspaces
	if ws == nil {
		return nil
	}
	var out []domain.StepName
	for _, s := range batch {
		if _, ok := ws.Steps[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// prepareWorkspaces computes the selection for the per-workspace steps of a
// batch about to run, on the canonical worktree as it stands now: after every
// earlier batch, so after every write an earlier step kept. Steps that keep
// writes are sequential barriers, so nothing changes the tree while this
// reads it. Batches without a per-workspace step cost nothing.
func (r *Runner) prepareWorkspaces(sels *workspaceSelections, hook domain.Hook, resolved domain.ResolvedPolicy, batch []domain.StepName, wt Worktree) error {
	steps := perWorkspaceSteps(resolved, batch)
	if len(steps) == 0 {
		return nil
	}
	if r.Workspaces == nil || wt == nil {
		return errors.New("per-workspace steps need workspace discovery, which this runner was not given")
	}
	sel, err := r.selectWorkspaces(hook, resolved, wt)
	if err != nil {
		return fmt.Errorf("select workspaces for %s: %w", domain.JoinSteps(steps), err)
	}
	for _, s := range steps {
		sels.set(s, sel)
	}
	return nil
}

// selectWorkspaces decides which workspaces this run's per-workspace steps
// cover, reading the graph from the tree the steps run against (design 0006):
//
//   - pre-push: every workspace (FullRulePrePush). An affected selection at
//     pre-push needs a full-scope anchor and a scope in the signed note,
//     which a later release adds;
//   - pre-commit with select: all: every workspace (FullRuleSelectAll);
//   - pre-commit with select: affected: every workspace when a step whose
//     writes are kept runs at or after the first per-workspace step
//     (FullRuleWriterOrder), since the tree committed would then differ from
//     the tree selected on; otherwise domain.Select over the graphs at HEAD
//     and at the worktree's tracked tree, the paths between them (the staged
//     change, plus any fix an earlier step kept) and the configuration
//     digests at HEAD and in force.
//
// Both graphs are limited to the ecosystems workspaces.discover names.
func (r *Runner) selectWorkspaces(hook domain.Hook, resolved domain.ResolvedPolicy, wt Worktree) (*domain.WorkspaceSelection, error) {
	ws := resolved.Workspaces
	tree, err := r.Workspaces.TrackedTree(wt.Dir())
	if err != nil {
		return nil, fmt.Errorf("read the checked tree: %w", err)
	}
	head, err := r.Workspaces.Discover(tree)
	if err != nil {
		return nil, err
	}
	head = head.Only(ws.Discover)
	full := func(rule domain.FullRule) (*domain.WorkspaceSelection, error) {
		sel, err := domain.FullSelection(head, domain.ForcedFull{Rule: rule})
		if err != nil {
			return nil, err
		}
		return &domain.WorkspaceSelection{Selection: sel, Graph: head}, nil
	}
	switch {
	case hook != domain.PreCommit:
		return full(domain.FullRulePrePush)
	case ws.Select != domain.WorkspaceSelectAffected:
		return full(domain.FullRuleSelectAll)
	case writerAtOrAfterPerWorkspace(resolved):
		return full(domain.FullRuleWriterOrder)
	}

	// The anchor is the commit the worktree was seeded from. Without one (no
	// commit yet) the base graph is empty and its configuration unknown,
	// which Select answers with a full selection.
	var base domain.WorkspaceGraph
	var anchorDigest string
	changed := []string{}
	if anchor, err := wt.HeadSHA(); err == nil && anchor != "" {
		if base, err = r.Workspaces.Discover(anchor); err != nil {
			return nil, fmt.Errorf("workspaces at HEAD: %w", err)
		}
		base = base.Only(ws.Discover)
		if changed, err = r.Workspaces.ChangedPaths(anchor, tree); err != nil {
			return nil, err
		}
		// An unreadable configuration at HEAD is an unknown one: Select
		// forces full rather than failing the commit.
		anchorDigest, _ = r.Workspaces.ConfigurationDigestAt(anchor)
	}
	sel, err := domain.Select(ws.SelectionInput(base, head, changed, anchorDigest, resolved.ConfigurationDigest))
	if err != nil {
		return nil, err
	}
	return &domain.WorkspaceSelection{Selection: sel, Graph: head}, nil
}

// writerAtOrAfterPerWorkspace reports whether a step whose writes are kept
// runs at or after the first per-workspace step (the design's ordering rule).
// A per-workspace step that keeps its own writes counts: its fixes land after
// the selection was made.
func writerAtOrAfterPerWorkspace(p domain.ResolvedPolicy) bool {
	first := slices.IndexFunc(p.Steps, func(s domain.StepName) bool {
		_, ok := p.Workspaces.Steps[s]
		return ok
	})
	if first < 0 {
		return false
	}
	return slices.ContainsFunc(p.Steps[first:], p.KeepsWrites)
}
