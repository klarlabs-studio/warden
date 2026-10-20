// Package feedback stores the background feedback document
// (warden.feedback/v1): the atomic writer, the per-worktree watcher lock, the
// snapshot a reader recomputes, and the recorder that builds the document
// from a staged evaluation's step events.
//
// Nothing that decides reads this package's output. The gate, verify,
// attest, notes and execution requirements never open status.json; a test
// enforces it.
package feedback

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.klarlabs.de/warden/internal/domain"
)

// maxDocumentBytes bounds what a reader will parse. Findings are capped per
// message, so a real document is far smaller.
const maxDocumentBytes = 16 << 20

// Dir is the per-worktree feedback directory under an absolute git dir.
func Dir(gitDir string) string { return filepath.Join(gitDir, "warden", "feedback") }

// StatusPath is where the document lives for the worktree whose
// (`--absolute-git-dir`) git dir is gitDir.
func StatusPath(gitDir string) string { return filepath.Join(Dir(gitDir), "status.json") }

// ErrNoDocument means no producer has written a document for this worktree.
var ErrNoDocument = errors.New("no background feedback document")

// Write replaces the document at path atomically: a reader sees the previous
// document or the new one, never a torn mix.
func Write(path string, doc domain.FeedbackDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "status-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

// Read loads and validates the document at path.
func Read(path string) (domain.FeedbackDocument, error) {
	var doc domain.FeedbackDocument
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, ErrNoDocument
	}
	if err != nil {
		return doc, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return doc, err
	}
	if len(data) > maxDocumentBytes {
		return doc, fmt.Errorf("%w: larger than %d bytes", domain.ErrInvalidFeedback, maxDocumentBytes)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, fmt.Errorf("%w: %v", domain.ErrInvalidFeedback, err)
	}
	if err := doc.Validate(); err != nil {
		return doc, err
	}
	return doc, nil
}
