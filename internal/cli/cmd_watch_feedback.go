package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/feedback"
	"go.klarlabs.de/warden/internal/infrastructure/watch"
	"go.klarlabs.de/warden/internal/service"
)

// feedbackHeartbeatInterval is how often the staged watcher refreshes its
// lock and the document's heartbeat. A variable so tests need not wait.
var feedbackHeartbeatInterval = domain.FeedbackHeartbeatInterval

// feedbackSession is the staged watcher's background feedback: the worktree
// lock, the recorder writing status.json, and the heartbeat keeping both
// fresh. Only the watcher creates one; a gate run never writes the document.
type feedbackSession struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	lock   *feedback.Lock
	rec    *feedback.Recorder
	stderr io.Writer
	done   chan struct{}

	mu  sync.Mutex
	env string
	// recording is whether the current evaluation's snapshot was recorded.
	recording bool
	// lastBeginErr suppresses repeating the same failure on every evaluation.
	lastBeginErr string
}

// startFeedback takes the worktree's watcher lock and starts the heartbeat.
// A second watcher on the same worktree gets exitContention, naming the
// holder's pid. On failure the session is nil and code is the exit code.
func startFeedback(ctx context.Context, svc *service.Service, dir string, stderr io.Writer) (session *feedbackSession, code int) {
	gitDir, err := svc.GitDir()
	if err != nil {
		return nil, fail(stderr, err)
	}
	lock, err := feedback.Acquire(gitDir)
	var held *feedback.HeldError
	if errors.As(err, &held) {
		_, _ = fmt.Fprintf(stderr, "warden: %v\n", err)
		return nil, exitContention
	}
	if err != nil {
		return nil, fail(stderr, fmt.Errorf("background feedback lock: %w", err))
	}
	// Not cancellable: a stop requested while starting is the loop's to
	// report, not a failure to find the worktree.
	worktree, err := feedback.Worktree(context.WithoutCancel(ctx), dir)
	if err != nil {
		_ = lock.Release()
		return nil, fail(stderr, err)
	}
	rec := feedback.NewRecorder(feedback.RecorderOptions{
		Path:          feedback.StatusPath(gitDir),
		WardenVersion: Version,
		Mode:          domain.FeedbackModeStaged,
		Worktree:      worktree,
		StartedAt:     lock.Holder().StartedAt,
		PID:           os.Getpid(),
	})
	sessionCtx, cancel := context.WithCancelCause(ctx)
	s := &feedbackSession{ctx: sessionCtx, cancel: cancel, lock: lock, rec: rec, stderr: stderr, done: make(chan struct{})}
	go s.heartbeat()
	return s, 0
}

func (s *feedbackSession) heartbeat() {
	defer close(s.done)
	ticker := time.NewTicker(feedbackHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.lock.Heartbeat(); errors.Is(err, feedback.ErrLockLost) {
				s.cancel(err)
				return
			}
			_ = s.rec.Heartbeat()
		}
	}
}

// lost reports whether another watcher took the lock over.
func (s *feedbackSession) lost() bool {
	return errors.Is(context.Cause(s.ctx), feedback.ErrLockLost)
}

// exitCode is code, unless the lock was lost: then this watcher no longer
// owns the document and stops with exitContention.
func (s *feedbackSession) exitCode(code int, stderr io.Writer) int {
	if s.lost() {
		_, _ = fmt.Fprintf(stderr, "warden: %v; this watcher stopped\n", feedback.ErrLockLost)
		return exitContention
	}
	return code
}

// close records a clean stop and releases the lock. A watcher whose lock was
// taken over writes nothing more: the document belongs to the new holder.
func (s *feedbackSession) close() {
	s.cancel(nil)
	<-s.done
	if s.lost() {
		return
	}
	_ = s.rec.Stop()
	_ = s.lock.Release()
}

func (s *feedbackSession) observer() application.Observer { return s.rec }

// observeEnvironment receives each environment the watcher sampled.
func (s *feedbackSession) observeEnvironment(env string) {
	s.mu.Lock()
	if env != "" {
		s.env = env
	}
	s.mu.Unlock()
	s.rec.EnvironmentObserved(environmentDigest(env))
}

func (s *feedbackSession) environment() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.env
}

// environmentDigest identifies a sampled environment; empty for none.
func environmentDigest(env string) string {
	if env == "" {
		return ""
	}
	return domain.DigestBytes([]byte(env))
}

// begin records the snapshot an evaluation is about to check. If it cannot
// be computed the evaluation still runs; it is just not recorded, and the
// document keeps describing its previous snapshot (which a reader then
// reports as stale).
func (s *feedbackSession) begin(ctx context.Context, svc *service.Service, dir string) error {
	s.mu.Lock()
	s.recording = false
	s.mu.Unlock()
	snapshot, steps, err := s.currentSnapshot(ctx, svc, dir)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		s.mu.Lock()
		repeat := s.lastBeginErr == err.Error()
		s.lastBeginErr = err.Error()
		s.mu.Unlock()
		if !repeat {
			_, _ = fmt.Fprintf(s.stderr, "warden: background feedback not recorded: %v\n", err)
		}
		return nil
	}
	s.mu.Lock()
	s.recording, s.lastBeginErr = true, ""
	s.mu.Unlock()
	s.rec.Begin(ctx, snapshot, steps)
	return nil
}

func (s *feedbackSession) currentSnapshot(ctx context.Context, svc *service.Service, dir string) (domain.FeedbackSnapshot, []domain.StepName, error) {
	cfg, err := svc.Config()
	if err != nil {
		return domain.FeedbackSnapshot{}, nil, err
	}
	digest, err := domain.DigestJSON(cfg)
	if err != nil {
		return domain.FeedbackSnapshot{}, nil, err
	}
	snapshot, err := feedback.CurrentSnapshot(ctx, dir, digest, environmentDigest(s.environment()))
	return snapshot, expectedStagedSteps(cfg), err
}

// expectedStagedSteps lists the pre-commit steps opted into the predictive
// cache, in pipeline order: the steps a staged evaluation is expected to run.
// Policy rules may narrow that; a step that never starts is dropped from the
// document when the evaluation ends.
func expectedStagedSteps(cfg domain.Config) []domain.StepName {
	names := cfg.Steps["pre_commit"]
	if len(names) == 0 {
		names = domain.DefaultSteps(domain.PreCommit)
	}
	var out []domain.StepName
	for _, name := range names {
		if slices.Contains(cfg.PredictiveCache, string(name)) && strings.TrimSpace(cfg.Commands[string(name)]) != "" {
			out = append(out, name)
		}
	}
	return out
}

// record writes the evaluation's outcome. A cancelled evaluation records
// nothing: it was superseded or stopped, and its snapshot is not complete.
func (s *feedbackSession) record(ctx context.Context, err error) {
	s.mu.Lock()
	recording := s.recording
	s.recording = false
	s.mu.Unlock()
	if !recording || ctx.Err() != nil {
		return
	}
	switch {
	case errors.Is(err, watch.ErrIdle):
		s.rec.Idle("nothing staged; no checks run")
	case watch.IsBlocked(err):
		s.rec.Blocked(err.Error())
	case err != nil:
		s.rec.Complete(err.Error())
	default:
		s.rec.Complete("")
	}
}

// observeLoop applies the loop's own transitions: yielding to a gate run
// cancels the evaluation, and the document says why nothing is running.
func (s *feedbackSession) observeLoop(e watch.Event) {
	if e.State == watch.StateYielded {
		s.rec.Paused("a gate run is in progress; background checks paused")
	}
}
