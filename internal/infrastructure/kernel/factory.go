package kernel

import (
	"context"
	"fmt"

	"go.klarlabs.de/axi"
	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Factory builds per-run kernels over axi-go, satisfying
// application.KernelFactory. It holds the registry of built-in step
// implementations to bind into each run.
type Factory struct {
	registry application.Registry
}

// NewFactory returns a Factory backed by the given built-in step registry.
func NewFactory(reg application.Registry) *Factory {
	return &Factory{registry: reg}
}

// New builds a fresh kernel for one run and wraps it with a run-level evidence
// ledger that hash-chains every step's evidence for the provenance note (§9).
func (f *Factory) New(policy domain.ResolvedPolicy, sc application.StepContext, priors *[]domain.Finding, push application.PushFunc) (application.Kernel, error) {
	var pf PushFunc
	if push != nil {
		pf = PushFunc(push)
	}
	k, err := Build(f.registry, policy, sc, priors, pf)
	if err != nil {
		return nil, err
	}
	ledger, err := axidomain.NewExecutionSession("warden-run", "warden.run", nil)
	if err != nil {
		return nil, fmt.Errorf("create run ledger: %w", err)
	}
	return &runKernel{k: k, ledger: ledger}, nil
}

// runKernel adapts axi's Kernel to application.Kernel. It records each step's
// evidence into a single ledger session so the whole run shares one
// tamper-evident chain, independent of axi's per-session chains.
type runKernel struct {
	k      *axi.Kernel
	ledger *axidomain.ExecutionSession
}

func (r *runKernel) Execute(ctx context.Context, step domain.StepName) (application.StepOutcome, error) {
	out, err := r.k.Execute(ctx, axi.Invocation{Action: string(step)})
	if err != nil {
		return application.StepOutcome{}, err
	}
	return r.absorb(step, out)
}

func (r *runKernel) Approve(ctx context.Context, sessionID, principal, rationale string) (application.StepOutcome, error) {
	out, err := r.k.Approve(ctx, sessionID, axidomain.ApprovalDecision{Principal: principal, Rationale: rationale})
	if err != nil {
		return application.StepOutcome{}, err
	}
	return r.absorb(domain.StepPush, out)
}

func (r *runKernel) Reject(ctx context.Context, sessionID, principal, rationale string) (application.StepOutcome, error) {
	out, err := r.k.Reject(ctx, sessionID, axidomain.ApprovalDecision{Principal: principal, Rationale: rationale})
	if err != nil {
		return application.StepOutcome{}, err
	}
	return r.absorb(domain.StepPush, out)
}

// absorb records the axi result's evidence into the run ledger and projects it
// to an application.StepOutcome. A Failed axi session (the executor returned a
// Go error, e.g. a push failure) is surfaced as an error rather than an in-band
// status, so the Runner's error handling catches it instead of mistaking a
// failed push for success.
func (r *runKernel) absorb(step domain.StepName, out *axi.Result) (application.StepOutcome, error) {
	for _, ev := range out.Evidence {
		r.ledger.AppendEvidence(ev)
	}
	if out.Status == axidomain.StatusFailed {
		msg := "execution failed"
		if out.Failure != nil {
			msg = out.Failure.Message
		}
		return application.StepOutcome{}, fmt.Errorf("%s: %s", step, msg)
	}
	sr, ok := stepResultFrom(out.Result)
	if !ok {
		// The action paused (awaiting_approval) before its executor ran, so no
		// StepResult exists yet; report an empty one.
		sr = domain.StepResult{Step: step, Status: domain.StepPass}
	}
	return application.StepOutcome{
		Result:        sr,
		NeedsApproval: out.Status == axidomain.StatusAwaitingApproval,
		SessionID:     string(out.SessionID),
	}, nil
}

// Finalize verifies the run-level chain and projects it to storable entries.
func (r *runKernel) Finalize() (string, []domain.EvidenceEntry, error) {
	if err := r.ledger.VerifyEvidenceChain(); err != nil {
		return "", nil, err
	}
	records := r.ledger.Evidence()
	entries := make([]domain.EvidenceEntry, 0, len(records))
	for _, ev := range records {
		entries = append(entries, domain.EvidenceEntry{
			Kind:         ev.Kind,
			Source:       ev.Source,
			Hash:         string(ev.Hash),
			PreviousHash: string(ev.PreviousHash),
			Timestamp:    ev.Timestamp,
		})
	}
	var root string
	if len(entries) > 0 {
		root = entries[0].Hash
	}
	return root, entries, nil
}
