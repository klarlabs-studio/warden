package application

import (
	"fmt"
	"strings"
	"sync"

	"go.klarlabs.de/warden/internal/domain"
)

// agentIsolation is the boundary the runner draws around the agent steps of
// one batch when a compliance profile isolates them (ADR 0007 §3): each such
// step runs in its own disposable clone, and the tracked state of the clone
// and of the validated worktree is captured before the step and compared
// after it. Without a profile, nothing is cloned or captured here, and the
// batch runs exactly as it did before profiles existed.
type agentIsolation struct {
	guards map[domain.StepName]*agentGuard
	// canonical is the validated worktree's state before the batch. Only set
	// when at least one step is guarded.
	canonical domain.TreeState
	wt        Worktree
	steps     []domain.StepName // guarded steps, in batch order, for messages

	mu      sync.Mutex
	checked map[domain.StepName]domain.StepResult
	// after is the validated worktree's state once the batch finished,
	// captured on first use.
	after *domain.TreeState
}

type agentGuard struct {
	clone   Worktree
	before  domain.TreeState
	profile string
}

// isolateAgents clones a worktree for every agent step in batch that its
// profile isolates, registers it for the step, and captures the tracked state
// of the clone and of wt. A clone that cannot be made fails the step: the
// result is returned instead of falling back to wt, where the agent could
// rewrite the tree that is about to be attested. The caller must call cleanup.
func isolateAgents(batch []domain.StepName, wt Worktree, reg *worktreeRegistry, resolved domain.ResolvedPolicy) (*agentIsolation, *domain.StepResult, error) {
	iso := &agentIsolation{guards: map[domain.StepName]*agentGuard{}, wt: wt, checked: map[domain.StepName]domain.StepResult{}}
	if wt == nil {
		return iso, nil, nil
	}
	for _, step := range batch {
		b := resolved.AgentBoundaryFor(step)
		if !b.Isolate {
			continue
		}
		clone, err := wt.Clone(resolved.MaterializeDeps)
		if err != nil {
			res := isolationFailure(step, b.Profile, "its disposable clone could not be created: "+err.Error())
			return iso, &res, nil
		}
		before, err := clone.TreeState()
		if err != nil {
			_ = clone.Remove()
			res := isolationFailure(step, b.Profile, "its clone's tracked state could not be read: "+err.Error())
			return iso, &res, nil
		}
		iso.guards[step] = &agentGuard{clone: clone, before: before, profile: b.Profile}
		iso.steps = append(iso.steps, step)
		reg.set(step, clone.Dir())
	}
	if len(iso.guards) > 0 {
		canonical, err := wt.TreeState()
		if err != nil {
			return iso, nil, fmt.Errorf("capture the validated worktree's state before %s: %w", domain.JoinSteps(iso.steps), err)
		}
		iso.canonical = canonical
	}
	return iso, nil, nil
}

// isolated reports whether step has its own guarded clone this batch.
func (iso *agentIsolation) isolated(step domain.StepName) bool {
	_, ok := iso.guards[step]
	return ok
}

// cleanup removes every clone isolateAgents made.
func (iso *agentIsolation) cleanup() {
	for _, g := range iso.guards {
		_ = g.clone.Remove()
	}
}

// checkClone applies the clone half of the guard to step's result: whether the
// agent changed HEAD, the index or a tracked file in its own clone. It is safe
// for concurrent use and computes each step once, so the live finish event and
// the folded outcome agree.
func (iso *agentIsolation) checkClone(step domain.StepName, res domain.StepResult) domain.StepResult {
	g, ok := iso.guards[step]
	if !ok {
		return res
	}
	iso.mu.Lock()
	defer iso.mu.Unlock()
	if done, ok := iso.checked[step]; ok {
		return done
	}
	var problem string
	after, err := g.clone.TreeState()
	switch {
	case err != nil:
		problem = "its clone's tracked state could not be read after it ran: " + err.Error()
	default:
		if d := g.before.Describe(after); d != "" {
			problem = "it " + d + " in its clone"
		}
	}
	res = observeGuard(res, g.profile, problem)
	iso.checked[step] = res
	return res
}

// checkCanonical applies the other half: the validated worktree, whose HEAD is
// the commit the note will attest, must be exactly as it was before the batch.
// When it is not, warden cannot tell which step in the batch changed it, so
// every guarded agent step is failed and none is reported as tree-unchanged.
func (iso *agentIsolation) checkCanonical(step domain.StepName, res domain.StepResult) (domain.StepResult, error) {
	g, ok := iso.guards[step]
	if !ok {
		return res, nil
	}
	if iso.after == nil {
		after, err := iso.wt.TreeState()
		if err != nil {
			return res, fmt.Errorf("capture the validated worktree's state after %s: %w", domain.JoinSteps(iso.steps), err)
		}
		iso.after = &after
	}
	if d := iso.canonical.Describe(*iso.after); d != "" {
		res = observeGuard(res, g.profile, "the validated worktree "+d+" while it ran")
	}
	return res, nil
}

// observeGuard records the guard's verdict on res: isolated always, unchanged
// only when there is no problem, and a failure naming the problem otherwise.
func observeGuard(res domain.StepResult, profile, problem string) domain.StepResult {
	obs := domain.AgentBoundaryObservation{}
	if res.AgentBoundary != nil {
		obs = *res.AgentBoundary
	}
	obs.Isolated = true
	obs.TreeUnchanged = problem == ""
	res.AgentBoundary = &obs
	if problem == "" {
		return res
	}
	msg := fmt.Sprintf("%s: %s; profile %s does not let an agent step change tracked files, the index or HEAD (untracked output is allowed)",
		res.Step, problem, profile)
	res.Status = domain.StepFail
	res.Blocker = domain.BlockerNone
	res.Findings = append(res.Findings, domain.Finding{Severity: domain.SeverityHigh, Message: msg})
	res.Summary = string(res.Step) + " failed: " + problem
	return res
}

// isolationFailure is the result for an agent step that could not be isolated.
// It never ran, and it does not fall back to the validated worktree.
func isolationFailure(step domain.StepName, profile, why string) domain.StepResult {
	msg := fmt.Sprintf("%s was not run: %s; profile %s runs agent steps only in a disposable clone", step, why, profile)
	return domain.StepResult{
		Step:          step,
		Status:        domain.StepFail,
		Outcome:       domain.StepSkipped,
		OutcomeReason: "could not isolate it",
		Findings:      []domain.Finding{{Severity: domain.SeverityHigh, Message: msg}},
		Summary:       strings.TrimSpace(string(step) + " not run: " + why),
	}
}
