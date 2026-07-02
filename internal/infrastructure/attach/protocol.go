// Package attach lets a running gate publish its live progress on a per-repo
// Unix socket so another terminal can watch it with `warden attach`. The run
// itself still blocks git (the hook contract is unchanged); the socket is a
// read-only side-channel, broadcast best-effort so a slow or absent watcher
// never slows the gate.
package attach

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// SocketPath is the per-repo socket location: a short, hashed name under the
// system temp dir. It is deliberately NOT under the git dir — a Unix socket path
// must fit the ~104-byte sun_path limit, which a deep repo path would blow, so
// the socket lives in a short-pathed temp dir keyed by a hash of the git dir
// (server and client derive the same path from the same git dir).
func SocketPath(gitDir string) string {
	sum := sha256.Sum256([]byte(gitDir))
	return filepath.Join(os.TempDir(), "warden-run-"+hex.EncodeToString(sum[:6])+".sock")
}

// Event is one newline-delimited JSON message on the wire. Type is "step" for a
// step lifecycle event or "done" for the terminal outcome.
type Event struct {
	Type     string           `json:"type"`
	Step     string           `json:"step,omitempty"`
	Phase    string           `json:"phase,omitempty"`
	Status   string           `json:"status,omitempty"`
	Line     string           `json:"line,omitempty"`
	Findings []domain.Finding `json:"findings,omitempty"`
	Outcome  string           `json:"outcome,omitempty"`
	Message  string           `json:"message,omitempty"`
}

// eventFromStep projects a StepEvent onto the wire form.
func eventFromStep(e application.StepEvent) Event {
	return Event{
		Type:     "step",
		Step:     string(e.Step),
		Phase:    string(e.Phase),
		Status:   string(e.Result.Status),
		Line:     e.Line,
		Findings: e.Result.Findings,
	}
}

// doneEvent is the terminal message a run publishes when it finishes.
func doneEvent(res application.RunResult) Event {
	return Event{Type: "done", Outcome: string(res.Outcome), Message: res.Message}
}
