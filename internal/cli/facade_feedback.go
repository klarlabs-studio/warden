package cli

import (
	"errors"

	"go.klarlabs.de/warden/internal/infrastructure/feedback"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// FeedbackStatus reads the background feedback document for the agent
// surfaces through the same reader as `warden feedback status`. A worktree
// with no document is an answer, not an error.
func (f facade) FeedbackStatus() (mcpserver.FeedbackStatusOutput, error) {
	doc, err := readFeedback(f.svc)
	if errors.Is(err, feedback.ErrNoDocument) {
		return mcpserver.FeedbackStatusOutput{Message: noFeedbackMessage}, nil
	}
	if err != nil {
		// An invalid document or an unreadable snapshot is something the
		// caller can act on, so the reason must reach it.
		return mcpserver.FeedbackStatusOutput{}, mcpserver.VisibleError(err)
	}
	diagnostics, note := feedback.Diagnosable(doc)
	return mcpserver.FeedbackStatusOutput{Present: true, Diagnostics: diagnostics, FreshnessNote: note, Document: &doc}, nil
}

func (u *unavailableFacade) FeedbackStatus() (mcpserver.FeedbackStatusOutput, error) {
	return mcpserver.FeedbackStatusOutput{}, u.err()
}
