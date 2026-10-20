package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/feedback"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// feedbackReadTimeout bounds the reader's git calls. The reader runs
// whenever an editor asks, so it must answer quickly or not at all.
const feedbackReadTimeout = 10 * time.Second

// cmdFeedback reads the background feedback the staged watcher writes.
//
// The reader validates the document, recomputes the worktree's current
// snapshot id and fills in freshness itself: a producer can only say what it
// observed, never whether that still holds. It never probes a container
// engine, and it never starts a watcher or runs a check.
func cmdFeedback(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		_, _ = fmt.Fprintln(stderr, "warden: usage: warden feedback status [--format json]")
		return 2
	}
	fs := flag.NewFlagSet("feedback status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "json", "output format (json)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "warden: usage: warden feedback status [--format json]")
		return 2
	}
	if *format != "json" {
		_, _ = fmt.Fprintf(stderr, "warden: unsupported feedback format %q (supported: json)\n", *format)
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		return fail(stderr, err)
	}
	doc, err := feedback.Read(feedback.StatusPath(gitDir))
	if errors.Is(err, feedback.ErrNoDocument) {
		_, _ = fmt.Fprintln(stderr, "warden: no background feedback for this worktree; start `warden watch --staged` to produce it")
		return 1
	}
	if err != nil {
		return fail(stderr, err)
	}
	cfg, err := svc.Config()
	if err != nil {
		return fail(stderr, fmt.Errorf("cannot determine the current snapshot: %w", err))
	}
	digest, err := domain.DigestJSON(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), feedbackReadTimeout)
	defer cancel()
	current, err := feedback.CurrentSnapshot(ctx, mustCwd(), digest, "")
	if err != nil {
		return fail(stderr, fmt.Errorf("cannot determine the current snapshot: %w", err))
	}
	doc.Freshness = doc.ComputeFreshness(domain.FeedbackObservation{
		Now:               time.Now(),
		CurrentSnapshotID: current.ID,
		ProducerAlive:     proc.Alive(doc.Producer.PID),
	})
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fail(stderr, err)
	}
	return 0
}
