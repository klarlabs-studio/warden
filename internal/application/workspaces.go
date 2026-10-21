package application

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.klarlabs.de/warden/internal/domain"
)

// workspaceSelections holds the selection each per-workspace step of the
// current batch runs over. The runner fills it before a batch starts; steps
// read it, possibly concurrently, through StepContext.Selection.
//
// It also keeps what the run's signed scope is built from: the tree each
// selection was computed on and the anchor an affected one was measured from.
type workspaceSelections struct {
	mu  sync.Mutex
	m   map[domain.StepName]*domain.WorkspaceSelection
	ctx map[domain.StepName]selectionContext
	// trustedKeys are the signers whose full-scope notes may anchor an
	// affected selection at pre-push. Empty means the run's own signer.
	trustedKeys []string
	// anchor caches the run's anchor search, so a second per-workspace batch
	// does not walk history again.
	anchor *anchorSearch
}

// selectionContext is where a selection came from.
type selectionContext struct {
	// tree is the tree object the selection was computed on.
	tree string
	// anchor is the full-scope run an affected selection was measured from;
	// nil for every other selection.
	anchor *AnchorCandidate
}

type anchorSearch struct {
	from      string
	candidate AnchorCandidate
	found     bool
}

func newWorkspaceSelections() *workspaceSelections {
	return &workspaceSelections{m: map[domain.StepName]*domain.WorkspaceSelection{}, ctx: map[domain.StepName]selectionContext{}}
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

func (w *workspaceSelections) set(step domain.StepName, sel *domain.WorkspaceSelection, ctx selectionContext) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[step] = sel
	w.ctx[step] = ctx
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
	sel, ctx, err := r.selectWorkspaces(sels, hook, resolved, wt)
	if err != nil {
		return fmt.Errorf("select workspaces for %s: %w", domain.JoinSteps(steps), err)
	}
	for _, s := range steps {
		sels.set(s, sel, ctx)
	}
	return nil
}

// selectWorkspaces decides which workspaces this run's per-workspace steps
// cover, reading the graph from the tree the steps run against (design 0006):
//
//   - select: all (either hook): every workspace (FullRuleSelectAll);
//   - select: affected, when a step whose writes are kept runs at or after
//     the first per-workspace step: every workspace (FullRuleWriterOrder),
//     since the tree committed would then differ from the tree selected on;
//   - pre-push with select: affected: every workspace under --attest-only
//     (FullRuleAttestOnly) or without a signer (FullRuleNoSigner), and when
//     no first-parent ancestor carries a trusted full-scope note that can
//     anchor the run (FullRuleNoAnchor); otherwise domain.Select over the
//     graphs at the anchor and at the checked tree, the paths between them
//     and the configuration digests (the anchor's, recorded in its scope);
//   - pre-commit with select: affected: domain.Select over the graphs at
//     HEAD and at the worktree's tracked tree, the paths between them (the
//     staged change, plus any fix an earlier step kept) and the
//     configuration digests at HEAD and in force.
//
// Both graphs are limited to the ecosystems workspaces.discover names.
func (r *Runner) selectWorkspaces(sels *workspaceSelections, hook domain.Hook, resolved domain.ResolvedPolicy, wt Worktree) (*domain.WorkspaceSelection, selectionContext, error) {
	ws := resolved.Workspaces
	tree, err := r.Workspaces.TrackedTree(wt.Dir())
	if err != nil {
		return nil, selectionContext{}, fmt.Errorf("read the checked tree: %w", err)
	}
	ctx := selectionContext{tree: tree}
	head, err := r.Workspaces.Discover(tree, ws.Discover)
	if err != nil {
		return nil, ctx, err
	}
	head = head.Only(ws.Discover)
	full := func(rule domain.FullRule) (*domain.WorkspaceSelection, selectionContext, error) {
		sel, err := domain.FullSelection(head, domain.ForcedFull{Rule: rule})
		if err != nil {
			return nil, ctx, err
		}
		return &domain.WorkspaceSelection{Selection: sel, Graph: head}, ctx, nil
	}
	switch {
	case ws.Select != domain.WorkspaceSelectAffected:
		return full(domain.FullRuleSelectAll)
	case writerAtOrAfterPerWorkspace(resolved):
		return full(domain.FullRuleWriterOrder)
	case hook == domain.PrePush:
		return r.selectAtPrePush(sels, resolved, wt, head, ctx, full)
	}

	// The anchor is the commit the worktree was seeded from. Without one (no
	// commit yet) the base graph is empty and its configuration unknown,
	// which Select answers with a full selection.
	var base domain.WorkspaceGraph
	var anchorDigest string
	changed := []string{}
	if anchor, err := wt.HeadSHA(); err == nil && anchor != "" {
		if base, err = r.Workspaces.Discover(anchor, ws.Discover); err != nil {
			return nil, ctx, fmt.Errorf("workspaces at HEAD: %w", err)
		}
		base = base.Only(ws.Discover)
		if changed, err = r.Workspaces.ChangedPaths(anchor, tree); err != nil {
			return nil, ctx, err
		}
		// An unreadable configuration at HEAD is an unknown one: Select
		// forces full rather than failing the commit.
		anchorDigest, _ = r.Workspaces.ConfigurationDigestAt(anchor)
	}
	sel, err := domain.Select(ws.SelectionInput(base, head, changed, anchorDigest, resolved.ConfigurationDigest))
	if err != nil {
		return nil, ctx, err
	}
	return &domain.WorkspaceSelection{Selection: sel, Graph: head}, ctx, nil
}

// AnchorSearchLimit bounds the first-parent walk for a full-scope anchor.
// Beyond it the run is full, and becomes the next anchor itself.
const AnchorSearchLimit = 500

// selectAtPrePush is the pre-push half of selectWorkspaces under select:
// affected, once the ordering rule holds. Every check that can force full
// runs BEFORE any step does: an affected selection is only worth computing
// when its note can be signed, and the signer is checked here, not after the
// steps ran on a selection nothing can vouch for.
func (r *Runner) selectAtPrePush(sels *workspaceSelections, resolved domain.ResolvedPolicy, wt Worktree, head domain.WorkspaceGraph, ctx selectionContext,
	full func(domain.FullRule) (*domain.WorkspaceSelection, selectionContext, error)) (*domain.WorkspaceSelection, selectionContext, error) {
	ws := resolved.Workspaces
	if r.Settings.AttestOnly {
		return full(domain.FullRuleAttestOnly)
	}
	if !r.canSign() {
		return full(domain.FullRuleNoSigner)
	}
	from, err := wt.HeadSHA()
	if err != nil || from == "" {
		return full(domain.FullRuleNoAnchor)
	}
	anchor, found, err := r.findAnchor(sels, resolved, from)
	if err != nil {
		return nil, ctx, err
	}
	if !found {
		return full(domain.FullRuleNoAnchor)
	}
	base, err := r.Workspaces.Discover(anchor.SHA, ws.Discover)
	if err != nil {
		return nil, ctx, fmt.Errorf("workspaces at the anchor %s: %w", short12(anchor.SHA), err)
	}
	base = base.Only(ws.Discover)
	changed, err := r.Workspaces.ChangedPaths(anchor.SHA, ctx.tree)
	if err != nil {
		return nil, ctx, err
	}
	sel, err := domain.Select(ws.SelectionInput(base, head, changed, anchor.Record.Scope.ConfigurationDigest, resolved.ConfigurationDigest))
	if err != nil {
		return nil, ctx, err
	}
	if sel.Kind == domain.SelectionAffected {
		ctx.anchor = &anchor
	}
	return &domain.WorkspaceSelection{Selection: sel, Graph: head}, ctx, nil
}

// canSign reports whether a signer is available to sign this run's note.
func (r *Runner) canSign() bool {
	return r.Signer != nil && strings.TrimSpace(r.Signer.PublicKey()) != ""
}

// findAnchor searches the first-parent history below from for the nearest
// commit whose note can anchor an affected selection of this run (see
// domain.RunRecord.QualifiesAsAnchor): a trusted, signed, full-scope run under
// the same configuration that ran every per-workspace step of this run from
// the same template. The signers trusted are the committed trusted_keys
// roster, or, without one, this machine's own signer.
func (r *Runner) findAnchor(sels *workspaceSelections, resolved domain.ResolvedPolicy, from string) (AnchorCandidate, bool, error) {
	if c := sels.anchor; c != nil && c.from == from {
		return c.candidate, c.found, nil
	}
	req := domain.AnchorRequirement{
		ConfigurationDigest: resolved.ConfigurationDigest,
		Steps:               map[domain.StepName]string{},
		TrustedKeys:         sels.trustedKeys,
	}
	if len(req.TrustedKeys) == 0 {
		req.TrustedKeys = []string{r.Signer.PublicKey()}
	}
	for _, step := range resolved.Steps {
		def, ok := resolved.Workspaces.Steps[step]
		if !ok {
			continue
		}
		digest, err := def.TemplateDigest()
		if err != nil {
			return AnchorCandidate{}, false, fmt.Errorf("template digest of %s: %w", step, err)
		}
		req.Steps[step] = digest
	}
	candidate, found, err := r.Git.FullScopeAnchor(r.Settings.Remote, from, AnchorSearchLimit, func(c AnchorCandidate) bool {
		return c.Record.QualifiesAsAnchor(c.SHA, c.Tree, req)
	})
	if err != nil {
		return AnchorCandidate{}, false, fmt.Errorf("search for a full-scope anchor: %w", err)
	}
	sels.anchor = &anchorSearch{from: from, candidate: candidate, found: found}
	return candidate, found, nil
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

// buildScope returns the scope a pre-push run's note records, or nil when it
// records none. Only a run under `workspaces.select: affected` whose
// per-workspace steps ran records one: a repository that selects all, or a run
// with no per-workspace step, writes a note byte-identical to before, which
// every verifier, old or new, reads as full.
//
// Every per-workspace step of one run must have been selected identically (the
// same selection on the same tree) for an affected scope: one scope speaks for
// all of them. The ordering rule guarantees it, since nothing that keeps writes
// runs between them; a run where it does not hold refuses to attest.
func buildScope(sels *workspaceSelections, resolved domain.ResolvedPolicy) (*domain.RunScope, error) {
	ws := resolved.Workspaces
	if ws == nil || ws.Select != domain.WorkspaceSelectAffected || resolved.Hook != domain.PrePush {
		return nil, nil
	}
	sels.mu.Lock()
	defer sels.mu.Unlock()
	var first domain.StepName
	steps := map[domain.StepName]domain.PerWorkspaceStep{}
	for _, step := range resolved.Steps {
		def, isPW := ws.Steps[step]
		if _, selected := sels.m[step]; !isPW || !selected {
			continue
		}
		if first == "" {
			first = step
		}
		steps[step] = def
	}
	if first == "" {
		return nil, nil
	}
	sel, ctx := sels.m[first], sels.ctx[first]
	want, err := sel.Selection.Digest()
	if err != nil {
		return nil, err
	}
	for step := range steps {
		other, otherCtx := sels.m[step], sels.ctx[step]
		got, err := other.Selection.Digest()
		if err != nil {
			return nil, err
		}
		same := got == want && otherCtx.tree == ctx.tree
		if !same && (sel.Selection.Kind != domain.SelectionFull || other.Selection.Kind != domain.SelectionFull) {
			return nil, fmt.Errorf("per-workspace steps %s and %s of one run were selected differently", first, step)
		}
	}
	in := domain.ScopeInput{
		Selection:           *sel,
		HeadTree:            ctx.tree,
		ConfigurationDigest: resolved.ConfigurationDigest,
		Steps:               steps,
	}
	if ctx.anchor != nil {
		in.Anchor, in.AnchorTree = ctx.anchor.SHA, ctx.anchor.Tree
	}
	scope, err := domain.NewRunScope(in)
	if err != nil {
		return nil, err
	}
	return &scope, nil
}
