package attach

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"go.klarlabs.de/warden/internal/domain"
)

// ErrNoRun is returned by Attach when no run is currently publishing (the socket
// is absent or refuses a connection).
var ErrNoRun = errors.New("no live warden run to attach to")

// Attach connects to the run socket and renders its event stream to w until the
// run ends (server closes the connection) or ctx is cancelled. It returns
// ErrNoRun when nothing is publishing.
func Attach(ctx context.Context, gitDir string, w io.Writer) error {
	conn, err := net.Dial("unix", SocketPath(gitDir))
	if err != nil {
		return ErrNoRun
	}
	defer conn.Close()

	// Unblock the blocking Read below when the caller cancels.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	fmt.Fprintln(w, "attached to warden run — live:")
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
		fmt.Fprintf(w, "\n%s — %s\n", ev.Outcome, ev.Message)
	case "step":
		switch ev.Phase {
		case "started":
			fmt.Fprintf(w, "▶ %s\n", ev.Step)
		case "output":
			fmt.Fprintf(w, "    %s\n", ev.Line)
		case "finished":
			glyph := "✓"
			if ev.Status == string(domain.StepFail) {
				glyph = "✗"
			}
			fmt.Fprintf(w, "%s %s\n", glyph, ev.Step)
			for _, f := range ev.Findings {
				loc := f.File
				if f.Line > 0 {
					loc = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				fmt.Fprintf(w, "    [%s] %s %s\n", f.Severity, loc, f.Message)
			}
		}
	}
}
