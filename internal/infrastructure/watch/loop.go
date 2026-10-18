package watch

import (
	"context"
	"errors"
	"time"
)

// Default retry bounds for an evaluation blocked by the machine rather than
// the change.
const (
	DefaultRetryMin = 5 * time.Second
	DefaultRetryMax = 60 * time.Second
)

// ErrIdle is returned by an evaluation that found nothing to check (nothing
// staged). The loop records the snapshot as done and reports StateIdle.
var ErrIdle = errors.New("nothing to check")

// blockedError marks an evaluation that could not run because of the machine
// (container engine down, a tool's lock held elsewhere). It is not a verdict
// about the snapshot.
type blockedError struct{ err error }

func (b blockedError) Error() string { return b.err.Error() }
func (b blockedError) Unwrap() error { return b.err }

// Blocked wraps err as an environmental failure: the loop retries the same
// snapshot with capped backoff instead of recording err as its result.
func Blocked(err error) error {
	if err == nil {
		return nil
	}
	return blockedError{err}
}

// IsBlocked reports whether err was marked with Blocked.
func IsBlocked(err error) bool {
	var b blockedError
	return errors.As(err, &b)
}

// State is what a reported Event says about the watched snapshot.
type State string

const (
	// StateComplete: an evaluation of the current snapshot finished; Err is
	// its result (nil for a pass).
	StateComplete State = "complete"
	// StateIdle: the current snapshot has nothing to check.
	StateIdle State = "idle"
	// StateBlocked: the machine prevented the evaluation; it is retried after
	// RetryIn.
	StateBlocked State = "blocked"
	// StateError: the snapshot could not be sampled at all.
	StateError State = "error"
)

// Event is one report from the loop.
type Event struct {
	State   State
	Err     error
	RetryIn time.Duration
}

// Loop coalesces changes until two samples agree, then evaluates that
// snapshot. A change during an evaluation cancels it; the replacement waits for
// teardown, so evaluations never overlap. Only evaluations of the snapshot that
// is still current are reported, and only a non-blocked result completes a
// snapshot.
type Loop struct {
	Interval    time.Duration
	Fingerprint func(context.Context) (string, error)
	Evaluate    func(context.Context) error
	// Report is optional.
	Report func(Event)
	// RetryMin and RetryMax bound the backoff for blocked evaluations; zero
	// means DefaultRetryMin / DefaultRetryMax.
	RetryMin, RetryMax time.Duration
}

// loopState is the mutable state of one Run.
type loopState struct {
	l                        Loop
	last, completed, running string
	lastError                string
	blocked                  string
	backoff                  time.Duration
	retryAt                  time.Time
	runContext               context.Context
	cancel                   context.CancelFunc
	done                     chan error
	retryMin, retryMax       time.Duration
}

// Run drives the loop until ctx is done.
func (l Loop) Run(ctx context.Context) {
	s := &loopState{l: l, retryMin: l.RetryMin, retryMax: l.RetryMax}
	if s.retryMin <= 0 {
		s.retryMin = DefaultRetryMin
	}
	if s.retryMax < s.retryMin {
		s.retryMax = max(DefaultRetryMax, s.retryMin)
	}
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()
	defer s.stop()
	s.sample(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-s.done:
			s.finish(err)
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

func (s *loopState) stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

func (s *loopState) report(e Event) {
	if s.l.Report != nil {
		s.l.Report(e)
	}
}

func (s *loopState) sample(ctx context.Context) {
	value, err := s.l.Fingerprint(ctx)
	if err != nil {
		if ctx.Err() == nil && s.lastError != err.Error() {
			s.report(Event{State: StateError, Err: err})
		}
		s.lastError = err.Error()
		s.last = ""
		if s.cancel != nil {
			s.cancel()
		}
		return
	}
	s.lastError = ""
	if value != s.last {
		s.last = value
		if s.cancel != nil && s.running != value {
			s.cancel()
		}
		return
	}
	if s.done != nil || value == "" || value == s.completed {
		return
	}
	if value == s.blocked && time.Now().Before(s.retryAt) {
		return
	}
	child, stop := context.WithCancel(ctx)
	s.runContext, s.cancel, s.running = child, stop, value
	s.done = make(chan error, 1)
	done := s.done
	go func() { done <- s.l.Evaluate(child) }()
}

func (s *loopState) finish(err error) {
	current := s.runContext.Err() == nil
	s.cancel()
	s.cancel, s.done = nil, nil
	if !current || s.running != s.last || s.last == "" {
		return // superseded: the next agreeing samples start the replacement
	}
	switch {
	case IsBlocked(err):
		if s.blocked == s.running {
			s.backoff = min(s.backoff*2, s.retryMax)
		} else {
			s.blocked, s.backoff = s.running, s.retryMin
		}
		s.retryAt = time.Now().Add(s.backoff)
		s.report(Event{State: StateBlocked, Err: err, RetryIn: s.backoff})
	case errors.Is(err, ErrIdle):
		s.completed, s.blocked = s.running, ""
		s.report(Event{State: StateIdle})
	default:
		s.completed, s.blocked = s.running, ""
		s.report(Event{State: StateComplete, Err: err})
	}
}
