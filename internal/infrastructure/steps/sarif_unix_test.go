//go:build unix

package steps

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The report is checked as a regular file, then opened. A FIFO swapped in
// between must be refused, not opened: opening a FIFO for reading blocks
// until a writer appears, which nothing in the worktree has to provide, so
// the step (and the gate) would hang forever.
func TestReadConfinedRefusesAFIFOSwappedInBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "lint.sarif")
	if err := os.WriteFile(report, []byte(`{"version":"2.1.0","runs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeConfinedOpen = func() {
		if err := os.Remove(report); err != nil {
			t.Error(err)
		}
		if err := syscall.Mkfifo(report, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeConfinedOpen = func() {} })

	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := readConfined(dir, "lint.sarif", diagnosticsStamp{})
		done <- outcome{err}
	}()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("a FIFO swapped in for the report was read")
		}
		if !strings.Contains(got.err.Error(), "replaced") && !strings.Contains(got.err.Error(), "regular") {
			t.Fatalf("unexpected refusal: %v", got.err)
		}
	case <-time.After(5 * time.Second):
		// Unblock the stuck open so the goroutine can finish.
		if w, err := os.OpenFile(report, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("opening the swapped-in FIFO blocked")
	}
}
