//go:build !windows

package cli

import (
	"io"
	"os"
)

// openControllingTTY opens the process's controlling terminal. It fails when
// there is none (ENXIO): CI, launchd, or a process spawned without a terminal.
func openControllingTTY() (io.ReadWriteCloser, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}
