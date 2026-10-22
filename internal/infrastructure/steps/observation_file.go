package steps

import (
	"fmt"
	"io"
	"os"
)

const maxObservationBytes = 4096

// The workload can replace this file. Confine resolution to the workspace,
// reject links and special files, and bound reads even if it grows concurrently.
// Nonblocking open on Unix prevents a raced-in FIFO from hanging cleanup.
func readContainerObservation(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("observation is not a regular file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|observationNonblock, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("observation file changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxObservationBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxObservationBytes {
		return nil, fmt.Errorf("observation exceeds %d bytes", maxObservationBytes)
	}
	return raw, nil
}
