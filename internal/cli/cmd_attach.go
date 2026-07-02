package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"go.klarlabs.de/warden/internal/infrastructure/attach"
)

// cmdAttach handles `warden attach`: connect to a currently-running gate's
// socket and stream its live progress, so a long pre-push started elsewhere (or
// whose TUI was closed) can be watched from another terminal. It is read-only —
// the run itself, not attach, drives git.
func cmdAttach(_ []string, stdout, stderr io.Writer) int {
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := attach.Attach(ctx, gitDir, stdout); err != nil {
		if errors.Is(err, attach.ErrNoRun) {
			fmt.Fprintln(stderr, "warden: no live run to attach to (start one with a push, or `warden run pre-push`).")
			return 1
		}
		if errors.Is(err, context.Canceled) {
			return 0
		}
		return fail(stderr, err)
	}
	return 0
}
