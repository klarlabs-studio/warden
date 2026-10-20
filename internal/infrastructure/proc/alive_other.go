//go:build !unix

package proc

import "os"

// Alive reports whether pid names a running process. On Windows FindProcess
// opens the process and fails when none has that pid. On platforms where it
// cannot tell (it always succeeds), the answer is "alive": callers pair this
// with a heartbeat, which is what eventually declares such a process gone.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
