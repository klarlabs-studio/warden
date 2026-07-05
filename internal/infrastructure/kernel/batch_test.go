package kernel

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// concurrentStep records how many peers are running when it executes, so a batch
// can prove its steps actually overlapped rather than ran one after another.
type concurrentStep struct {
	name     domain.StepName
	inFlight *int32
	peakSeen *int32
	release  <-chan struct{}
}

func (s *concurrentStep) Name() domain.StepName { return s.name }

func (s *concurrentStep) Run(_ context.Context, _ application.StepContext) (domain.StepResult, error) {
	n := atomic.AddInt32(s.inFlight, 1)
	for { // track the high-water mark of concurrent steps
		peak := atomic.LoadInt32(s.peakSeen)
		if n <= peak || atomic.CompareAndSwapInt32(s.peakSeen, peak, n) {
			break
		}
	}
	<-s.release // hold the slot until every sibling has entered
	atomic.AddInt32(s.inFlight, -1)
	return domain.StepResult{
		Step:     s.name,
		Status:   domain.StepPass,
		Findings: []domain.Finding{{Severity: domain.SeverityLow, Message: string(s.name)}},
		Summary:  "ran",
	}, nil
}

// panicStep panics inside Run, standing in for a step (or the executor it
// drives) that blows up while running in a parallel batch worker.
type panicStep struct{ name domain.StepName }

func (s panicStep) Name() domain.StepName { return s.name }
func (s panicStep) Run(context.Context, application.StepContext) (domain.StepResult, error) {
	panic("boom in a parallel step")
}

// TestExecuteBatch_RecoversStepPanic proves a panicking step in a parallel batch
// is turned into a per-step error rather than an unrecovered goroutine panic —
// which would crash the whole gate and skip the runner's deferred worktree
// teardown (leaking the isolated worktree).
func TestExecuteBatch_RecoversStepPanic(t *testing.T) {
	names := []domain.StepName{domain.StepTest, domain.StepLint}
	reg := application.Registry{
		domain.StepTest: &fakeStep{name: domain.StepTest, status: domain.StepPass},
		domain.StepLint: panicStep{name: domain.StepLint},
	}
	p := policyFor(names...)
	p.Commands = map[string]string{}
	k, err := NewFactory(reg).New(p, application.StepContext{}, new([]domain.Finding), nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = k.ExecuteBatch(context.Background(), names, nil)
	if err == nil {
		t.Fatal("a panicking step must surface as an error, not crash the process")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), string(domain.StepLint)) {
		t.Errorf("error should name the panicking step, got: %v", err)
	}
}

func TestExecuteBatch_RunsConcurrentlyAndFoldsEvidenceInOrder(t *testing.T) {
	var inFlight, peak int32
	release := make(chan struct{})
	names := []domain.StepName{domain.StepTest, domain.StepLint, "security-scan"}

	reg := application.Registry{}
	for _, n := range names {
		reg[n] = &concurrentStep{name: n, inFlight: &inFlight, peakSeen: &peak, release: release}
	}
	p := policyFor(names...)
	p.Commands = map[string]string{} // "security-scan" resolves to the registered fake, not a shell
	k, err := NewFactory(reg).New(p, application.StepContext{}, new([]domain.Finding), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Release the steps once they should all be in flight; if ExecuteBatch ran
	// them sequentially the first step would block forever waiting on a sibling.
	go func() {
		deadline := time.After(2 * time.Second)
		for {
			if atomic.LoadInt32(&inFlight) == int32(len(names)) {
				close(release)
				return
			}
			select {
			case <-deadline:
				close(release) // unblock so the test fails on the assertion, not a hang
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	var finished []domain.StepName
	var mu sync.Mutex
	onFinish := func(step domain.StepName, _ application.StepOutcome) {
		mu.Lock()
		finished = append(finished, step)
		mu.Unlock()
	}

	outcomes, err := k.ExecuteBatch(context.Background(), names, onFinish)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if peak != int32(len(names)) {
		t.Errorf("steps did not run concurrently: peak in-flight = %d, want %d", peak, len(names))
	}
	// Outcomes come back in steps order regardless of completion order.
	for i, oc := range outcomes {
		if oc.Result.Step != names[i] {
			t.Errorf("outcome[%d] = %s, want %s (order not preserved)", i, oc.Result.Step, names[i])
		}
	}
	if len(finished) != len(names) {
		t.Errorf("onFinish fired %d times, want %d", len(finished), len(names))
	}

	// Evidence is folded in steps order: each step contributes a summary record
	// then its finding, so the chain is deterministic.
	root, entries, err := k.Finalize()
	if err != nil {
		t.Fatalf("evidence chain must verify: %v", err)
	}
	if root == "" {
		t.Fatal("expected a verified evidence chain root")
	}
	var order []string
	for _, e := range entries {
		if e.Kind[len(e.Kind)-len(".finding"):] != ".finding" { // summary records only
			order = append(order, e.Source)
		}
	}
	want := []string{"test", "lint", "security-scan"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Errorf("evidence summary order = %v, want %v", order, want)
			break
		}
	}
}
