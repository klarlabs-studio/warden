package attach

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// ErrNoRun is returned by Attach when no run is currently publishing (the socket
// is absent or refuses a connection).
var ErrNoRun = errors.New("no live warden run to attach to")

// probeTimeout bounds Live's connection attempt. A local Unix socket answers
// in microseconds; a listener that does not accept within this is not one a
// caller should wait on.
const probeTimeout = 250 * time.Millisecond

// Live reports whether a gate run is publishing on gitDir's socket right now.
// It only connects and hangs up: nothing is read and the run is not slowed. A
// socket file left behind by a crashed run refuses the connection and reads as
// no run, so a stale file never pauses anybody. When the socket file is absent
// (the common case) this costs one stat.
func Live(gitDir string) bool {
	path := SocketPath(gitDir)
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", path, probeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Attach connects to the run socket and renders its event stream to w until the
// run ends (server closes the connection) or ctx is cancelled. It returns
// ErrNoRun when nothing is publishing.
func Attach(ctx context.Context, gitDir string, w io.Writer) error {
	conn, err := net.Dial("unix", SocketPath(gitDir))
	if err != nil {
		return ErrNoRun
	}
	defer func() { _ = conn.Close() }()

	// Unblock the blocking Read below when the caller cancels.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	_, _ = fmt.Fprintln(w, "attached to warden run — live:")
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil // EOF: run ended
		}
		render(w, ev)
		if ev.Type == "done" {
			return nil
		}
	}
}

// render writes one event as a human-readable line.
func render(w io.Writer, ev Event) {
	switch ev.Type {
	case "done":
		_, _ = fmt.Fprintf(w, "\n%s — %s\n", ev.Outcome, ev.Message)
	case "step":
		switch ev.Phase {
		case "started":
			_, _ = fmt.Fprintf(w, "▶ %s\n", ev.Step)
		case "output":
			_, _ = fmt.Fprintf(w, "    %s\n", ev.Line)
		case "finished":
			glyph := "✓"
			if ev.Status == string(domain.StepFail) {
				glyph = "✗"
			}
			line := glyph + " " + ev.Step
			if ev.StepOutcome != "" {
				line += ": " + domain.OutcomeNote{Outcome: domain.StepOutcome(ev.StepOutcome), Reason: ev.StepReason}.String()
			}
			_, _ = fmt.Fprintln(w, line)
			for _, f := range ev.Findings {
				loc := f.File
				if f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				_, _ = fmt.Fprintf(w, "    [%s] %s %s\n", f.Severity, loc, f.Message)
			}
		}
	}
}
