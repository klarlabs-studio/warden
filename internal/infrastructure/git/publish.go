package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// FastForwardTo advances local branch to sha, but only if branch still points
// at expectedTip. The compare-then-move guard is Warden's protection against a
// concurrent commit landing during a run: if the tip has changed, moving the
// ref would silently discard that work, so it returns ErrBranchMoved instead
// (§4.3).
func (r *Repo) FastForwardTo(branch, sha, expectedTip string) error {
	current, err := r.run("rev-parse", branch)
	if err != nil {
		return err
	}
	if current != expectedTip {
		return fmt.Errorf("%w: %s is at %s, expected %s", ErrBranchMoved, branch, current, expectedTip)
	}
	// update-ref with the old value makes the move atomic: git itself re-checks
	// the tip, closing the race between our rev-parse above and the write.
	ref := "refs/heads/" + branch
	if _, err := r.run("update-ref", ref, sha, expectedTip); err != nil {
		return err
	}
	return nil
}

// Push publishes branch to remote. This is the terminal external action of a
// passing pre-push run (§4.3).
// ApplyPatch applies a unified diff to the working tree (used to re-apply
// pre-commit auto-fixes computed in the worktree, §4.2). An empty patch is a
// no-op. Fixes land in the working tree, not the index, preserving whatever the
// developer had and had not staged.
func (r *Repo) ApplyPatch(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	// The patch is captured raw and already ends with a newline; only append one
	// if a caller passed a trimmed patch, never double it.
	if !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	cmd := gitCmd(r.Dir, "apply", "--binary")
	cmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Push publishes branch to remote with --no-verify: Warden performs this push
// itself only after its own pipeline has already validated the change, so the
// pre-push hook must be bypassed — otherwise the push would re-trigger Warden
// and recurse indefinitely (§4.3).
func (r *Repo) Push(remote, branch string) error {
	_, err := r.run("push", "--no-verify", remote, branch)
	return err
}

// WriteNote attaches rec as JSON to commit sha under refs/notes/warden. The -f
// flag overwrites any prior note so a re-validated commit reflects its latest
// run (§9).
func (r *Repo) WriteNote(sha string, rec domain.RunRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("git: marshal run record: %w", err)
	}
	_, err = r.run("notes", "--ref=warden", "add", "-f", "-m", string(payload), sha)
	return err
}

// ReadNote returns the warden run record on sha, or (nil, nil) when the commit
// carries no note. Absence is not an error: most commits predate adoption or
// were made outside Warden.
func (r *Repo) ReadNote(sha string) (*domain.RunRecord, error) {
	out, err := r.run("notes", "--ref=warden", "show", sha)
	if err != nil {
		// `git notes show` exits non-zero when no note exists; treat that as a
		// clean miss rather than a failure.
		return nil, nil
	}
	var rec domain.RunRecord
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		return nil, fmt.Errorf("git: unmarshal run record for %s: %w", sha, err)
	}
	return &rec, nil
}

// PushNotes publishes refs/notes/warden to remote so provenance travels with a
// shared branch (§9).
func (r *Repo) PushNotes(remote string) error {
	// --no-verify for the same reason as Push: a notes push would otherwise
	// re-trigger the pre-push hook.
	_, err := r.run("push", "--no-verify", remote, NotesRef+":"+NotesRef)
	return err
}

// FetchNotes retrieves refs/notes/warden from remote, letting doctor verify
// provenance written by other machines (§9).
func (r *Repo) FetchNotes(remote string) error {
	_, err := r.run("fetch", remote, NotesRef+":"+NotesRef)
	return err
}

// CommitsSince returns the SHAs reachable from ref back to (but excluding)
// adoptionSHA, newest first. This scopes doctor's audit to commits made after
// Warden was adopted, so pre-adoption history isn't flagged as unverified (§9).
func (r *Repo) CommitsSince(ref, adoptionSHA string) ([]string, error) {
	out, err := r.run("rev-list", adoptionSHA+".."+ref)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// CommitMeta returns the author, ISO-8601 commit date, and subject line for a
// commit, formatted for human-readable doctor output. A NUL separator keeps
// the fields unambiguous even when a value contains other whitespace.
func (r *Repo) CommitMeta(sha string) (author, date, subject string, err error) {
	out, err := r.run("show", "-s", "--format=%an%x00%cI%x00%s", sha)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(out, "\x00")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("git: unexpected commit meta for %s: %q", sha, out)
	}
	return parts[0], parts[1], parts[2], nil
}
