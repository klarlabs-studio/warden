package mcpserver

import (
	"go.klarlabs.de/mcp"

	"go.klarlabs.de/warden/internal/domain"
)

// feedbackReader is the background-feedback half of Facade. It lives in its
// own file because the feedback document is advisory and unsigned: a boundary
// test confines the code allowed to name it to the readers, and keeping the
// tool here keeps the rest of this package (run_trigger, verify) checked.
type feedbackReader interface {
	// FeedbackStatus reads the worktree's background feedback document, with
	// freshness judged now. A worktree with no document is not an error: the
	// output says so.
	FeedbackStatus() (FeedbackStatusOutput, error)
}

// FeedbackStatusInput takes no arguments; the document is per worktree, and
// the server's working directory names the worktree.
type FeedbackStatusInput struct{}

// FeedbackStatusOutput is what the background watcher last observed, judged now.
type FeedbackStatusOutput struct {
	// Present is false when no watcher has written a document for the
	// worktree; Message then says how to produce one.
	Present bool   `json:"present"`
	Message string `json:"message,omitempty"`
	// Diagnostics reports whether the document's results describe the
	// worktree as it is now and may be acted on as current. FreshnessNote
	// says why not, or what to bear in mind (an expired prediction).
	Diagnostics   bool   `json:"diagnostics"`
	FreshnessNote string `json:"freshness_note,omitempty"`
	// Document is the warden.feedback/v1 document with freshness filled in.
	Document *domain.FeedbackDocument `json:"document,omitempty"`
}

const feedbackStatusDescription = "Read what the background watcher (`warden watch --staged`, or `warden watch` under container execution) last " +
	"observed for this worktree: its state, the snapshot it checked, each step's status, findings " +
	"and container environment. freshness is computed now by the reader: anything but current " +
	"(or expired, which concerns only passing steps) means the results do not describe the " +
	"worktree as it is; diagnostics says whether they may be acted on as current. Advisory only: " +
	"the gate never reads this document, and a pass here is not provenance. This never starts a " +
	"watcher or runs a check."

// registerFeedbackTool adds the read-only feedback_status tool.
func registerFeedbackTool(srv *mcp.Server, f Facade) {
	srv.Tool("feedback_status").
		Description(feedbackStatusDescription).
		ReadOnly().
		Handler(func(FeedbackStatusInput) (FeedbackStatusOutput, error) {
			return f.FeedbackStatus()
		})
}
