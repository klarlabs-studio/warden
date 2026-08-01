//go:build unix

package proc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Isolate's whole purpose is a claim line coverage cannot make. Every step runs
// as `sh -c`, and the work the developer cares about — go test, tsc, an agent
// CLI — is a GRANDCHILD of that shell. exec's own cancellation signals only the
// shell, so without a process-group kill a timed-out step leaves its real
// workload running past the run and past the worktree teardown.
//
// "syscall.Kill was reached" is therefore not the property under test. These
// tests start a real grandchild and assert it is gone.

// alive reports whether pid still exists. Signal 0 performs the permission and
// existence checks without delivering anything.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone polls until pid disappears, so the test does not depend on how
// promptly the kernel reaps after SIGKILL.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !alive(pid)
}

// startWithGrandchild runs a shell that spawns a long-lived grandchild, writes
// the grandchild's pid where the test can read it, and then blocks. The
// returned pid is the grandchild's.
func startWithGrandchild(t *testing.T, ctx context.Context, isolate bool) (*exec.Cmd, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// `sleep 300 & echo $! > pidfile` makes the sleep a child of the shell —
	// i.e. a grandchild of this test process — then the shell waits, so both
	// are alive when the context is cancelled.
	script := "sleep 300 & echo $! > " + pidFile + "; wait"
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	if isolate {
		Isolate(cmd)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The pid file appears once the shell has forked; poll rather than sleep a
	// fixed amount so this is not timing-fragile on a loaded machine.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 && alive(pid) {
				return cmd, pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild never reported its pid")
	return nil, 0
}

func TestIsolateKillsTheGrandchildOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, pid := startWithGrandchild(t, ctx, true)

	cancel()
	_ = cmd.Wait() // cancellation makes this return an error; the pid is the assertion

	if !waitGone(pid, 15*time.Second) {
		// Do not leak a `sleep 300` if the guarantee regressed.
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("grandchild survived cancellation: a timed-out step would leave its real workload running")
	}
}

// Without Isolate the grandchild survives. This is not a test of the standard
// library so much as proof that the test above can actually fail — it pins the
// behavior Isolate exists to change, so the assertion is not vacuous.
func TestWithoutIsolateTheGrandchildSurvives(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, pid := startWithGrandchild(t, ctx, false)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	cancel()
	_ = cmd.Wait()

	// Give it the same grace the isolated case gets; it should still be there.
	time.Sleep(500 * time.Millisecond)
	if !alive(pid) {
		t.Skip("grandchild died without Isolate; this platform reaps differently and the contrast test does not apply")
	}
}

// Isolate installs a Cancel hook, and os/exec refuses to start a command that
// has one unless it came from CommandContext. That makes Isolate's doc comment
// ("expected to have been created with exec.CommandContext") load-bearing
// rather than advisory: pairing it with a plain exec.Command yields a command
// that cannot run at all. Pinned here so the requirement is discoverable from
// the tests and not only from a failed Start.
func TestIsolateRequiresCommandContext(t *testing.T) {
	cmd := exec.Command("true")
	Isolate(cmd)

	err := cmd.Start()
	if err == nil {
		_ = cmd.Wait()
		t.Skip("os/exec no longer rejects a non-CommandContext command with a Cancel hook")
	}
	if !strings.Contains(err.Error(), "Cancel") {
		t.Errorf("Start() = %v, want a complaint about the Cancel hook", err)
	}
}

func TestIsolateSetsProcessGroupAndWaitDelay(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	Isolate(cmd)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Isolate did not request a new process group; a group kill would signal nothing")
	}
	if cmd.WaitDelay != killGrace {
		t.Errorf("WaitDelay = %s, want %s: a grandchild holding the output pipe would block Wait forever", cmd.WaitDelay, killGrace)
	}
	if cmd.Cancel == nil {
		t.Fatal("Isolate left the default Cancel, which signals only the leader")
	}
}

// Cancel runs on a command that was never started when the context is already
// done by the time exec would launch it. It must report success rather than
// dereference a nil Process.
func TestCancelBeforeStartIsANoOp(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	Isolate(cmd)

	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel() on an unstarted command = %v, want nil", err)
	}
}

// A process group that has already exited makes syscall.Kill return ESRCH. The
// documented fallback is a single-process kill so cancellation still makes
// progress rather than surfacing an error for work that is already finished.
func TestCancelOnAlreadyExitedProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	Isolate(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	// Both the group and the process are gone; Cancel must not panic, and must
	// not report a failure to kill something that no longer exists as fatal.
	if err := cmd.Cancel(); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Cancel() after exit = %v, want nil, ESRCH or ErrProcessDone", err)
	}
}
