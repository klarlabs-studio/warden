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
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/feedback"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
	"go.klarlabs.de/warden/internal/service"
)

// feedbackReadTimeout bounds the reader's git calls. The reader runs
// whenever an editor asks, so it must answer quickly or not at all.
const feedbackReadTimeout = 10 * time.Second

const feedbackUsage = "warden: usage: warden feedback status [--format json|sarif|problems]"

// noFeedbackMessage is what every reader surface says when no watcher has
// written a document for the worktree.
const noFeedbackMessage = "no background feedback for this worktree; start `warden watch --staged` to produce it"

// cmdFeedback reads the background feedback the staged watcher writes.
//
// The reader validates the document, recomputes the worktree's current
// snapshot id and fills in freshness itself: a producer can only say what it
// observed, never whether that still holds. It never probes a container
// engine, and it never starts a watcher or runs a check.
func cmdFeedback(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		_, _ = fmt.Fprintln(stderr, feedbackUsage)
		return 2
	}
	fs := flag.NewFlagSet("feedback status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "json", "output format: json, sarif or problems")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, feedbackUsage)
		return 2
	}
	switch *format {
	case "json", "sarif", "problems":
	default:
		_, _ = fmt.Fprintf(stderr, "warden: unsupported feedback format %q (supported: json, sarif, problems)\n", *format)
		return 2
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		return fail(stderr, err)
	}
	doc, err := readFeedback(svc)
	if errors.Is(err, feedback.ErrNoDocument) {
		_, _ = fmt.Fprintln(stderr, "warden: "+noFeedbackMessage)
		return 1
	}
	if err != nil {
		return fail(stderr, err)
	}
	switch *format {
	case "sarif":
		err = feedback.RenderSARIF(stdout, doc)
	case "problems":
		err = feedback.RenderProblems(stdout, doc, commandAnchors(svc, stderr))
	default:
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(doc)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

// readFeedback loads the worktree's document and judges its freshness now.
// It is the one reader every surface (the command, MCP, axi) goes through.
func readFeedback(svc *service.Service) (domain.FeedbackDocument, error) {
	gitDir, err := svc.GitDir()
	if err != nil {
		return domain.FeedbackDocument{}, err
	}
	doc, err := feedback.Read(feedback.StatusPath(gitDir))
	if err != nil {
		return domain.FeedbackDocument{}, err
	}
	cfg, err := svc.Config()
	if err != nil {
		return domain.FeedbackDocument{}, fmt.Errorf("cannot determine the current snapshot: %w", err)
	}
	digest, err := domain.DigestJSON(cfg)
	if err != nil {
		return domain.FeedbackDocument{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), feedbackReadTimeout)
	defer cancel()
	current, err := feedback.CurrentSnapshot(ctx, mustCwd(), digest, "")
	if err != nil {
		return domain.FeedbackDocument{}, fmt.Errorf("cannot determine the current snapshot: %w", err)
	}
	doc.Freshness = doc.ComputeFreshness(domain.FeedbackObservation{
		Now:               time.Now(),
		CurrentSnapshotID: current.ID,
		ProducerAlive:     proc.Alive(doc.Producer.PID),
	})
	return doc, nil
}

// commandAnchors finds where each step's command is configured, so the
// problems renderer can anchor a failure that has no location of its own.
// Anchors are a convenience: when they cannot be read, the failures are still
// printed, unanchored, and the reason goes to stderr.
func commandAnchors(svc *service.Service, stderr io.Writer) map[domain.StepName]feedback.ConfigAnchor {
	repo := svc.Repo()
	if repo == nil {
		return nil
	}
	found, err := config.NewRepository(repo.Dir).CommandAnchors()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warden: failures are not anchored to the configuration: %v\n", err)
		return nil
	}
	anchors := make(map[domain.StepName]feedback.ConfigAnchor, len(found))
	for step, a := range found {
		anchors[domain.StepName(step)] = feedback.ConfigAnchor{File: a.File, Line: a.Line}
	}
	return anchors
}
