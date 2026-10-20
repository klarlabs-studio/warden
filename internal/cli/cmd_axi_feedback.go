package cli

import (
	"flag"
	"io"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// axiFeedback is `warden axi feedback`: what the background watcher last
// observed, judged now, in TOON. It exits 1 when no document exists, so a
// shell caller can tell "nothing observed" from an observation.
func axiFeedback(f facade, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("axi feedback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if rejectExtraArgs(fs, stderr, "axi feedback", "") {
		return 2
	}
	out, err := f.FeedbackStatus()
	if err != nil {
		return fail(stderr, err)
	}
	if !out.Present {
		if code := emitTOON(stdout, stderr, map[string]any{"present": false, "message": out.Message}); code != 0 {
			return code
		}
		return 1
	}
	doc := out.Document
	steps := make([]any, 0, len(doc.Steps))
	for i := range doc.Steps {
		s := &doc.Steps[i]
		m := map[string]any{"step": string(s.Step), "status": string(s.Status), "summary": s.Summary}
		if s.Blocker != "" {
			m["blocker"] = string(s.Blocker)
		}
		if s.Prediction.ExpiresAt != nil {
			m["prediction_expires_at"] = s.Prediction.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if env := s.Environment; env != nil {
			m["image_id"] = env.ImageID
			m["runtime"] = env.Runtime
		}
		if len(s.Findings) > 0 {
			findings := make([]any, 0, len(s.Findings))
			for _, fd := range s.Findings {
				row := findingMaps([]domain.Finding{fd.Finding})[0].(map[string]any)
				row["source"] = string(fd.Source)
				findings = append(findings, row)
			}
			m["findings"] = findings
		}
		steps = append(steps, m)
	}
	result := map[string]any{
		"present":     true,
		"state":       string(doc.State),
		"freshness":   string(doc.Freshness),
		"diagnostics": out.Diagnostics,
		"mode":        doc.Mode,
		"worktree":    doc.Worktree,
		"branch":      doc.Snapshot.Branch,
		"tree":        doc.Snapshot.Tree,
		"base":        doc.Snapshot.Base,
		"message":     doc.Message,
		"steps":       steps,
	}
	if out.FreshnessNote != "" {
		result["freshness_note"] = out.FreshnessNote
	}
	if doc.EvaluatedAt != nil {
		result["evaluated_at"] = doc.EvaluatedAt.UTC().Format(time.RFC3339)
	}
	return emitTOON(stdout, stderr, result)
}
