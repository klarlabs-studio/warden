//go:build !unix

package proc

import "os/exec"

// Isolate on non-unix platforms (Windows) can't use unix process-group signals,
// so it applies only the portable safeguard: a WaitDelay so a lingering child
// that kept an output pipe open after cancellation can't block Wait forever.
// The context's default Cancel (killing the process) still applies; a full
// job-object group kill is a future enhancement. It must be called before the
// command is started.
func Isolate(cmd *exec.Cmd) {
	cmd.WaitDelay = killGrace
}
