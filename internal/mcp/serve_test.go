package mcpserver

import (
	"context"
	"testing"
	"time"
)

// TestServe_ReturnsOnCancelledContext guards that Serve honors context
// cancellation rather than blocking forever, and exercises the Serve wrapper.
func TestServe_ReturnsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before Serve starts

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &fakeFacade{}, "test") }()

	select {
	case <-done:
		// Returned promptly — the contract we care about.
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return on a canceled context")
	}
}
