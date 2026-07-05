// Package proc holds process-management helpers shared by the step
// implementations. Warden runs configured commands (lint, test, agents, custom
// steps) as `sh -c` subprocesses, and those in turn spawn their own children
// (go test, tsc, an agent CLI). The unix build uses process-group primitives to
// kill the whole tree on cancellation; other platforms fall back to the
// portable pieces (see proc_other.go).
package proc

import "time"

// killGrace bounds how long os/exec waits for a step's output pipes to drain
// after its context is cancelled. A grandchild that inherited the pipe and
// outlived the kill would otherwise block Wait forever; after this delay exec
// closes the pipes and returns.
const killGrace = 5 * time.Second
