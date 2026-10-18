package watch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// events collects loop reports for assertions from the test goroutine.
type events struct {
	mu   sync.Mutex
	list []Event
}

func (e *events) add(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) snapshot() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.list...)
}

func runLoop(t *testing.T, l Loop) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	finished := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("loop did not stop")
		}
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An environmental failure is not a verdict: the same snapshot is retried with
// growing backoff until the machine recovers, and only then completes.
func TestLoopRetriesBlockedSnapshotWithBackoff(t *testing.T) {
	var calls, healthyAfter atomic.Int32
	healthyAfter.Store(3)
	var got events
	runLoop(t, Loop{
		Interval:    2 * time.Millisecond,
		Fingerprint: func(context.Context) (string, error) { return "snapshot", nil },
		Evaluate: func(context.Context) error {
			if calls.Add(1) < healthyAfter.Load() {
				return Blocked(errors.New("engine unavailable"))
			}
			return nil
		},
		Report:   got.add,
		RetryMin: 20 * time.Millisecond,
		RetryMax: 30 * time.Millisecond,
	})
	eventually(t, "completion after recovery", func() bool {
		evs := got.snapshot()
		return len(evs) > 0 && evs[len(evs)-1].State == StateComplete
	})
	evs := got.snapshot()
	if len(evs) != 3 || evs[0].State != StateBlocked || evs[1].State != StateBlocked || evs[2].Err != nil {
		t.Fatalf("events: %+v", evs)
	}
	if evs[0].RetryIn != 20*time.Millisecond || evs[1].RetryIn != 30*time.Millisecond {
		t.Fatalf("backoff not capped/doubled: %v, %v", evs[0].RetryIn, evs[1].RetryIn)
	}
	// Completed: no further evaluations of the same snapshot.
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 3 {
		t.Fatalf("evaluated %d times", n)
	}
}

// A real failure completes the snapshot: it is reported once and not retried.
func TestLoopCompletesOnVerdict(t *testing.T) {
	var calls atomic.Int32
	var got events
	runLoop(t, Loop{
		Interval:    2 * time.Millisecond,
		Fingerprint: func(context.Context) (string, error) { return "snapshot", nil },
		Evaluate: func(context.Context) error {
			calls.Add(1)
			return errors.New("lint failed")
		},
		Report: got.add,
	})
	eventually(t, "a verdict", func() bool { return len(got.snapshot()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 || got.snapshot()[0].State != StateComplete {
		t.Fatalf("calls %d events %+v", calls.Load(), got.snapshot())
	}
}

func TestLoopReportsIdleOnce(t *testing.T) {
	var got events
	var calls atomic.Int32
	runLoop(t, Loop{
		Interval:    2 * time.Millisecond,
		Fingerprint: func(context.Context) (string, error) { return "empty", nil },
		Evaluate: func(context.Context) error {
			calls.Add(1)
			return ErrIdle
		},
		Report: got.add,
	})
	eventually(t, "idle", func() bool { return len(got.snapshot()) == 1 })
	time.Sleep(50 * time.Millisecond)
	if evs := got.snapshot(); calls.Load() != 1 || evs[0].State != StateIdle || evs[0].Err != nil {
		t.Fatalf("calls %d events %+v", calls.Load(), evs)
	}
}

// A change during an evaluation supersedes it: the stale run is not reported
// and the new snapshot is evaluated.
func TestLoopEvaluatesChangeMadeDuringRun(t *testing.T) {
	var state atomic.Int32
	state.Store(1)
	started := make(chan int32, 8)
	var got events
	runLoop(t, Loop{
		Interval:    2 * time.Millisecond,
		Fingerprint: func(context.Context) (string, error) { return fmt.Sprint(state.Load()), nil },
		Evaluate: func(ctx context.Context) error {
			v := state.Load()
			started <- v
			if v == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		Report: got.add,
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no first evaluation")
	}
	state.Store(2)
	select {
	case v := <-started:
		if v != 2 {
			t.Fatalf("re-evaluated %d", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the change made during the run was never evaluated")
	}
	eventually(t, "the new snapshot's report", func() bool { return len(got.snapshot()) == 1 })
}

func TestLoopReportsFingerprintErrorsOnce(t *testing.T) {
	var got events
	runLoop(t, Loop{
		Interval:    2 * time.Millisecond,
		Fingerprint: func(context.Context) (string, error) { return "", errors.New("no index") },
		Evaluate:    func(context.Context) error { t.Error("evaluated without a snapshot"); return nil },
		Report:      got.add,
	})
	eventually(t, "the error", func() bool { return len(got.snapshot()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if evs := got.snapshot(); len(evs) != 1 || evs[0].State != StateError {
		t.Fatalf("events: %+v", evs)
	}
}

func TestBlockedWrapping(t *testing.T) {
	if Blocked(nil) != nil {
		t.Fatal("Blocked(nil) must be nil")
	}
	base := errors.New("engine down")
	err := fmt.Errorf("evaluate: %w", Blocked(base))
	if !IsBlocked(err) || !errors.Is(err, base) || err.Error() != "evaluate: engine down" {
		t.Fatalf("wrapping lost: %v", err)
	}
	if IsBlocked(base) {
		t.Fatal("an ordinary error is not blocked")
	}
}
