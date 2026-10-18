package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer safe for a watcher goroutine to write while
// the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitFor polls cond until it holds or the deadline passes, so a broken watcher
// fails the test instead of hanging it.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// TestLiveWatchChecksAnEditMadeDuringARun is the swallowed-edit regression.
// The loop used to run checks, then fingerprint the tree AFTER the run, so an
// edit saved while a check was running became the new baseline and was never
// checked. The check here records the content it saw; the edited content must
// show up in a later evaluation.
func TestLiveWatchChecksAnEditMadeDuringARun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell check")
	}
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "seen.log")
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := []namedCommand{{step: "test", command: "cat file.txt >> '" + log + "'; sleep 1"}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		runLiveWatch(ctx, dir, cmds, io.Discard)
		close(stopped)
	}()

	waitFor(t, 10*time.Second, "the first evaluation to start", func() bool {
		return strings.Contains(readFile(t, log), "before")
	})
	// Saved while the first check is still sleeping.
	if err := os.WriteFile(file, []byte("edited during the run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the edit to be evaluated", func() bool {
		return strings.Contains(readFile(t, log), "edited during the run")
	})
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("live watch did not stop")
	}
}
