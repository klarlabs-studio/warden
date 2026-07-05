package kernel

import (
	"context"
	"fmt"
	"sync"

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
	cache    application.StepCache
}

// NewFactory returns a Factory backed by the given built-in step registry.
func NewFactory(reg application.Registry) *Factory {
	return &Factory{registry: reg}
}

// WithCache attaches a step cache so cacheable steps can be skipped when their
// declared inputs are unchanged. Returns the factory for chaining.
func (f *Factory) WithCache(c application.StepCache) *Factory {
	f.cache = c
	return f
}

// New builds a fresh kernel for one run and wraps it with a run-level evidence
// ledger that hash-chains every step's evidence for the provenance note (§9).
func (f *Factory) New(policy domain.ResolvedPolicy, sc application.StepContext, priors *[]domain.Finding, push application.PushFunc) (application.Kernel, error) {
	var pf PushFunc
	if push != nil {
		pf = PushFunc(push)
	}
	k, err := Build(f.registry, policy, sc, priors, pf, f.cache)
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

// ExecuteBatch runs steps concurrently against the shared kernel, then folds
// their evidence into the run ledger in steps order so the provenance chain is
// identical to a sequential run. axi's session repositories are mutex-guarded,
// and the run ledger is only touched from this serial second pass, so the only
// concurrency is the steps' own work (the slow part). onFinish fires from each
// worker as it lands, letting the UI show staggered completion.
func (r *runKernel) ExecuteBatch(ctx context.Context, steps []domain.StepName, onFinish func(domain.StepName, application.StepOutcome)) ([]application.StepOutcome, error) {
	outs := make([]*axi.Result, len(steps))
	projected := make([]application.StepOutcome, len(steps))
	errs := make([]error, len(steps))

	var wg sync.WaitGroup
	for i, step := range steps {
		wg.Add(1)
		go func(i int, step domain.StepName) {
			defer wg.Done()
			// A step (or the executor it drives) that panics must become a
			// per-step error, not an unrecovered goroutine panic that crashes
			// the whole gate and skips the runner's worktree teardown.
			defer func() {
				if rec := recover(); rec != nil {
					errs[i] = fmt.Errorf("step %s panicked: %v", step, rec)
				}
			}()
			out, err := r.k.Execute(ctx, axi.Invocation{Action: string(step)})
			if err != nil {
				errs[i] = err
				return
			}
			outs[i] = out
			oc, perr := project(step, out)
			projected[i], errs[i] = oc, perr
			if perr == nil && onFinish != nil {
				onFinish(step, oc)
			}
		}(i, step)
	}
	wg.Wait()

	// Serial second pass: append evidence in steps order (before surfacing any
	// error, matching absorb) so the run's chain is deterministic.
	outcomes := make([]application.StepOutcome, len(steps))
	for i, step := range steps {
		if outs[i] != nil {
			for _, ev := range outs[i].Evidence {
				r.ledger.AppendEvidence(ev)
			}
		}
		if errs[i] != nil {
			return nil, fmt.Errorf("step %s: %w", step, errs[i])
		}
		outcomes[i] = projected[i]
	}
	return outcomes, nil
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
// to an application.StepOutcome. It is the sequential path; ExecuteBatch folds
// evidence itself so it can parallelize the work.
func (r *runKernel) absorb(step domain.StepName, out *axi.Result) (application.StepOutcome, error) {
	for _, ev := range out.Evidence {
		r.ledger.AppendEvidence(ev)
	}
	return project(step, out)
}

// project maps an axi result to an application.StepOutcome without touching the
// run ledger, so it is safe to call from concurrent workers (ExecuteBatch folds
// the evidence in serially afterward). A Failed axi session (the executor
// returned a Go error, e.g. a push failure) surfaces as an error rather than an
// in-band status, so the Runner's error handling catches it instead of mistaking
// a failed push for success.
func project(step domain.StepName, out *axi.Result) (application.StepOutcome, error) {
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
