//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// Isolate makes cmd the leader of its own process group and, when the command's
// context is cancelled (a per-step timeout or a Ctrl-C/SIGTERM at the gate),
// SIGKILLs the whole group rather than just the `sh` that CommandContext would
// signal on its own. Without this a timed-out step leaves its real workload
// (the `go test`, `tsc`, or agent CLI the shell forked) running detached, past
// the run and the worktree's teardown.
//
// It must be called before the command is started. cmd is expected to have been
// created with exec.CommandContext so the Cancel hook is wired to the context.
func Isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	// Cancel replaces exec's default (SIGKILL to the leader only) with a
	// group kill. The negative pid targets the whole process group; because
	// Setpgid makes the child a group leader, its pid is the group id.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// The group may already be gone (ESRCH) — fall back to the
			// single-process kill so cancellation still makes progress.
			return cmd.Process.Kill()
		}
		return nil
	}

	// Ensure Wait can't hang on a grandchild that kept an output pipe open
	// after the group was killed.
	cmd.WaitDelay = killGrace
}
