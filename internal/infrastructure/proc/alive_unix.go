//go:build unix

package proc

import (
	"errors"
	"syscall"
)

// Alive reports whether pid names a running process. Signal 0 checks for
// existence without delivering anything; EPERM means the process exists but
// belongs to someone else. A pid can be reused, so callers pair this with a
// heartbeat rather than trusting it alone.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
