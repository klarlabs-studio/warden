//go:build unix

package steps

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadContainerObservationRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "observation"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No writer exists. Opening this FIFO normally would block forever.
	if _, err := readContainerObservation(dir, "observation"); err == nil {
		t.Fatal("accepted FIFO")
	}
}
