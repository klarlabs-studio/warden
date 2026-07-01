// Package explain renders a resolved Warden policy as an XState v5-compatible
// statechart JSON document (spec §4.4 / §7). The chart is a linear pipeline —
// one state per resolved step, each advancing to the next on a synthetic
// "pass" event and ending in a terminal final state — so `warden policy
// explain` output can be pasted straight into any XState visualizer.
//
// Why a statechart rather than an ad-hoc diagram: Warden already models a hook
// run as a sequence of gated steps, which maps cleanly onto a state machine.
// Reusing statekit's builder and XState exporter gives us a single, validated
// representation that visual tools understand for free, and keeps the "explain"
// output faithful to the runtime execution order.
package explain

import (
	"go.klarlabs.de/statekit"
	"go.klarlabs.de/statekit/export"

	"go.klarlabs.de/warden/internal/domain"
)

// passEvent is the synthetic event that advances the pipeline from one step to
// the next. Every non-terminal state exposes exactly this transition, so the
// visualized chart reads as a straight line of "step passed → next step".
const passEvent statekit.EventType = "pass"

// approvalState is the ID of the human-approval pause inserted before the
// terminal state when the policy requires sign-off (§5.2 RequireApproval).
const approvalState statekit.StateID = "awaiting_approval"

// noop is the entry-action body. Actions carry no behavior here: the chart is
// for visualization only, and statekit requires every referenced action to be
// registered, so we register descriptive names against a shared no-op.
func noop(_ *struct{}, _ statekit.Event) {}

// terminalState returns the final-state ID for a hook: a pre-push run that
// passes ends in "pushed", a pre-commit run in "committed". This mirrors the
// terminal write-external action the daemon performs on a full pass (§4.3).
func terminalState(h domain.Hook) statekit.StateID {
	if h == domain.PreCommit {
		return "committed"
	}
	return "pushed"
}

// entryActionName builds a human-readable entry-action name that encodes the
// step's resolved agent and auto-fix budget. Encoding this into the action name
// (rather than an opaque behavior) makes the exported chart self-documenting:
// the reader sees, per state, which agent runs it and how many auto-fix retries
// it is granted.
func entryActionName(p domain.ResolvedPolicy, step domain.StepName) statekit.ActionType {
	agent := p.AgentFor(step)
	if agent == "" {
		agent = "default"
	}
	budget := p.AutoFixBudget(step)
	name := "run " + string(step) + " [agent=" + agent + " autofix=" + itoa(budget) + "]"
	return statekit.ActionType(name)
}

// itoa renders a small non-negative int without pulling in strconv for a single
// use; auto-fix budgets are tiny by nature.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Chart renders the resolved policy as an XState v5-compatible statechart JSON
// string: one state per step in sequence, each transitioning to the next on a
// synthetic "pass" event, ending in a terminal "pushed" (pre-push) or
// "committed" (pre-commit) final state. When RequireApproval is true, an
// "awaiting_approval" state is inserted before the terminal state. Each step
// state's entry action names its resolved agent and auto-fix budget, so the
// chart is self-documenting.
func Chart(p domain.ResolvedPolicy) (string, error) {
	terminal := terminalState(p.Hook)

	// Assemble the ordered list of node IDs the run walks through: every step,
	// then the optional approval pause, then the terminal state. Building the
	// full sequence first lets us wire each node to its successor uniformly and
	// handles the empty-steps case (just the terminal, optionally preceded by
	// approval) without special branches.
	var flow []statekit.StateID
	for _, step := range p.Steps {
		flow = append(flow, statekit.StateID(step))
	}
	if p.RequireApproval {
		flow = append(flow, approvalState)
	}
	flow = append(flow, terminal)

	machine := statekit.NewMachine[struct{}]("warden-" + string(p.Hook)).
		WithInitial(flow[0])

	for i, id := range p.Steps {
		state := machine.State(statekit.StateID(id))
		action := entryActionName(p, id)
		machine.WithAction(action, noop)
		state.OnEntry(action).
			On(passEvent).Target(flow[i+1])
	}

	// The approval pause, when present, is a plain state that advances to the
	// terminal on the same "pass" event, representing the reviewer's sign-off.
	if p.RequireApproval {
		machine.State(approvalState).
			On(passEvent).Target(terminal)
	}

	// The terminal state is final and has no outgoing transitions.
	machine.State(terminal).Final()

	cfg, err := machine.Build()
	if err != nil {
		return "", err
	}

	return export.NewXStateExporter(cfg).ExportJSONIndent("", "  ")
}
