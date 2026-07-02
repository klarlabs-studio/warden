package attach

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// safeBuffer is a concurrency-safe writer for the client goroutine.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// clientCount exposes the registered client count for test synchronization.
func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func TestServerClientRoundTrip(t *testing.T) {
	gitDir := t.TempDir()
	srv := NewServer(gitDir)
	if srv == nil {
		t.Fatal("NewServer returned nil — socket could not be created")
	}
	defer srv.Close()

	var buf safeBuffer
	done := make(chan error, 1)
	go func() { done <- Attach(context.Background(), gitDir, &buf) }()

	// Wait for the client to register before broadcasting, else early events drop.
	deadline := time.Now().Add(2 * time.Second)
	for srv.clientCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("client never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}

	srv.OnStep(application.StepEvent{Step: "test", Phase: application.StepStarted})
	srv.OnStep(application.StepEvent{Step: "test", Phase: application.StepOutput, Line: "=== RUN"})
	srv.OnStep(application.StepEvent{Step: "test", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "test", Status: domain.StepPass}})
	srv.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished,
		Result: domain.StepResult{Step: "lint", Status: domain.StepFail,
			Findings: []domain.Finding{{Severity: domain.SeverityHigh, File: "a.go", Line: 3, Message: "bad"}}}})
	srv.PublishDone(application.RunResult{Outcome: domain.OutcomePassed, Message: "pushed"})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Attach: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Attach did not return after the done event")
	}

	out := buf.String()
	for _, want := range []string{"▶ test", "=== RUN", "✓ test", "✗ lint", "a.go:3", "bad", "passed", "pushed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q:\n%s", want, out)
		}
	}
}

func TestAttach_NoRun(t *testing.T) {
	// No server listening → ErrNoRun.
	if err := Attach(context.Background(), t.TempDir(), &safeBuffer{}); err != ErrNoRun {
		t.Errorf("expected ErrNoRun, got %v", err)
	}
}

func TestServer_NilIsNoOp(t *testing.T) {
	var s *Server // NewServer can return nil; every method must tolerate it
	s.OnStep(application.StepEvent{})
	s.PublishDone(application.RunResult{})
	s.Close()
}
