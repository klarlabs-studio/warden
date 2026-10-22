package steps

import (
	"errors"
	"fmt"
	"strconv"

	"go.klarlabs.de/warden/internal/domain"
)

// compatLimitArgs turns execution.limits into Docker-compatible run flags
// (docker, podman and nerdctl share them). Swap is capped at the memory limit
// so a check cannot page past it. The flags are a request: an engine may
// ignore one (rootless podman on cgroup v1 warns and does), so nothing here is
// evidence that a limit held.
func compatLimitArgs(l domain.ExecutionLimits) ([]string, error) {
	mib, err := l.MemoryMiB()
	if err != nil {
		return nil, err
	}
	milli, err := l.MilliCPUs()
	if err != nil {
		return nil, err
	}
	var args []string
	if mib > 0 {
		size := strconv.FormatInt(mib, 10) + "m"
		args = append(args, "--memory="+size, "--memory-swap="+size)
	}
	if milli > 0 {
		args = append(args, "--cpus="+formatMilliCPUs(milli))
	}
	if l.Pids > 0 {
		args = append(args, "--pids-limit="+strconv.Itoa(l.Pids))
	}
	return args, nil
}

// errApplePids is why the apple runtime is refused for a pids limit: its CLI
// has no per-container process limit to apply.
var errApplePids = errors.New("cannot apply execution.limits.pids: the apple runtime has no process limit")

// appleLimitArgs turns execution.limits into Apple container flags. Apple sizes
// the container's VM: memory in whole MiB, whole CPUs, and no process limit. A
// limit it cannot apply is an error, so selection moves on to another runtime
// rather than run the check unbounded.
func appleLimitArgs(l *domain.ExecutionLimits) ([]string, error) {
	if l == nil {
		return nil, nil
	}
	mib, err := l.MemoryMiB()
	if err != nil {
		return nil, err
	}
	milli, err := l.MilliCPUs()
	if err != nil {
		return nil, err
	}
	if l.Pids > 0 {
		return nil, errApplePids
	}
	var args []string
	if mib > 0 {
		args = append(args, "--memory", strconv.FormatInt(mib, 10)+"M")
	}
	if milli > 0 {
		if milli%1000 != 0 {
			return nil, fmt.Errorf("cannot apply execution.limits.cpus %q: the apple runtime allocates whole CPUs", l.CPUs)
		}
		args = append(args, "--cpus", strconv.FormatInt(milli/1000, 10))
	}
	return args, nil
}

// formatMilliCPUs renders thousandths of a CPU as the shortest decimal.
func formatMilliCPUs(milli int64) string {
	return strconv.FormatFloat(float64(milli)/1000, 'f', -1, 64)
}

// observationProbe is the shell the workload runs before the configured
// command: it writes what it can read of its own bounds to path, one key=value
// per line (see domain.ParseContainerObservation). Only shell builtins are
// used, since an image need not ship more than /bin/sh and cat. It never fails
// the check: a value it cannot read is left out, and the record then says it
// was not observed.
//
// It reads cgroup v2 only, from the workload's own cgroup root, and counts
// IPv4 routes for the network. A check command runs after it in the same
// writable workspace and could overwrite the file; ADR 0008 lists that
// residual.
func observationProbe(path string) string {
	return `{ if [ -f /sys/fs/cgroup/cgroup.controllers ]; then echo cgroup=v2;` +
		` for f in memory.max memory.swap.max cpu.max pids.max; do v=;` +
		` if read -r v < "/sys/fs/cgroup/$f" || [ -n "$v" ]; then echo "$f=$v"; fi; done; fi 2>/dev/null;` +
		` if [ -r /proc/net/route ]; then n=0; { read -r _; while read -r _; do n=$((n+1)); done; } < /proc/net/route && echo "routes=$n"; fi 2>/dev/null;` +
		` } > ` + shellQuote(path) + " 2>/dev/null || true\n"
}
