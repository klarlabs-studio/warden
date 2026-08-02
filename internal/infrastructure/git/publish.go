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
// and recurse indefinitely. It is the terminal external action of a passing
// pre-push run (§4.3).
func (r *Repo) Push(remote, branch string, force domain.PushForce) error {
	args := []string{"push", "--no-verify"}
	// A rewrite needs an explicit force, because warden owns the push: git's
	// pre-push hook is told nothing about the developer's --force flag, so a
	// plain push here fails as non-fast-forward no matter what they typed.
	if force == domain.ForceLease {
		if lease := r.remoteTrackingSHA(remote, branch); lease != "" {
			// Pin the lease to the REMOTE-TRACKING ref — what this clone last
			// fetched — not to the remote's live value. Pinning to the live value
			// would make the lease vacuously true and degrade it to a bare --force,
			// discarding exactly the protection it exists for: a commit someone
			// else pushed since our last fetch must invalidate the rewrite.
			args = append(args, "--force-with-lease="+branch+":"+lease)
		}
	}
	_, err := r.run(append(args, remote, branch)...)
	return err
}

// remoteTrackingSHA returns this clone's last-fetched value for remote/branch,
// or "" when no such ref exists (a branch that has never been pushed). It
// cannot fail: an absent ref is a clean miss, not an error, so callers get a
// plain string rather than an error they would only ever compare against nil.
func (r *Repo) remoteTrackingSHA(remote, branch string) string {
	out, err := r.run("rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// PushSpanBase returns the commit this push starts from: the newest commit
// reachable from branch that the remote ALREADY has. Everything after it in
// (base, branch] is what this push publishes, which is exactly the span a
// passing run vouches for (see domain.RunRecord.CoversFrom).
//
// It is computed against every ref under refs/remotes/<remote>/, not just this
// branch's tracking ref, so it stays correct after a rebase: the rebased
// commits are all new to the remote, and the boundary lands on the merge-base
// with whatever the remote already had. Returns "" when no boundary exists
// (an unrelated or entirely new history), which records no span rather than
// guessing one.
func (r *Repo) PushSpanBase(remote, branch string) (string, error) {
	out, err := r.run("rev-list", "--boundary", branch, "--not", "--remotes="+remote)
	if err != nil {
		return "", err
	}
	// `--boundary` prefixes with '-' the commits just outside the set — i.e. the
	// ones the remote already has. The first is the nearest.
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "-"); ok {
			return rest, nil
		}
	}
	return "", nil
}

// CommitsInSpan lists the commits in (base, head], i.e. those a push with this
// span published. An empty base yields nothing: a span warden could not
// determine covers nothing, rather than everything.
func (r *Repo) CommitsInSpan(base, head string) ([]string, error) {
	if base == "" || head == "" {
		return nil, nil
	}
	out, err := r.run("rev-list", base+".."+head)
	if err != nil {
		return nil, err
	}
	var shas []string
	for line := range strings.SplitSeq(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			shas = append(shas, s)
		}
	}
	return shas, nil
}

// PushRewritesHistory reports whether pushing branch to remote would discard
// commits the remote currently has — i.e. the remote tip is not an ancestor of
// the local tip. That is the signal warden uses in place of the --force flag it
// never sees. A branch with no remote-tracking ref (never pushed) rewrites
// nothing. Errors resolve to false: warden must not force on a guess.
func (r *Repo) PushRewritesHistory(remote, branch string) (bool, error) {
	remoteTip := r.remoteTrackingSHA(remote, branch)
	if remoteTip == "" {
		return false, nil
	}
	local, err := r.run("rev-parse", "--verify", branch)
	if err != nil {
		return false, err
	}
	// Exit 0 means remoteTip is an ancestor of local: an ordinary fast-forward.
	if _, err := r.run("merge-base", "--is-ancestor", remoteTip, strings.TrimSpace(local)); err != nil {
		return true, nil
	}
	return false, nil
}

// UnmergedRemoteCommits lists commits on remote/branch with no patch-equivalent
// in the local branch, using `git cherry` — the same patch-id equivalence rebase
// itself uses to drop commits already applied upstream.
//
// This is the distinction PushRewritesHistory cannot make. "The remote tip is
// not an ancestor of ours" is true both when we rebased our OWN commits (the
// remote holds their pre-rewrite form, and force-pushing loses nothing) and when
// someone else pushed new work to a shared branch (force-pushing destroys it).
// git cherry marks the first case '-' (an equivalent patch exists locally) and
// the second '+'. Only '+' commits are at risk.
//
// --force-with-lease does NOT cover this: the lease only asserts that the remote
// has not moved since our last fetch, so once we have fetched the colleague's
// commit the lease is satisfied and the push destroys it anyway.
func (r *Repo) UnmergedRemoteCommits(remote, branch string) ([]string, error) {
	remoteRef := remote + "/" + branch
	if _, err := r.run("rev-parse", "--verify", "--quiet", remoteRef+"^{commit}"); err != nil {
		return nil, nil // no remote branch yet: nothing can be lost
	}
	out, err := r.run("cherry", branch, remoteRef)
	if err != nil {
		return nil, err
	}
	var lost []string
	for line := range strings.SplitSeq(out, "\n") {
		sha, ok := strings.CutPrefix(strings.TrimSpace(line), "+ ")
		if !ok {
			continue // "- <sha>": an equivalent patch is already in our history
		}
		sha = strings.TrimSpace(sha)
		if r.isOurPreRewriteCommit(branch, sha) {
			continue
		}
		subject, err := r.run("log", "-1", "--format=%h %s", sha)
		if err != nil {
			subject = sha
		}
		lost = append(lost, strings.TrimSpace(subject))
	}
	return lost, nil
}

// reflogScanLimit bounds how far back a branch's reflog is searched. Its own
// pre-rewrite tips are the most recent entries, so the answer is found in the
// first few; the limit exists so a long-lived branch cannot turn this into a
// walk of thousands of ancestry checks.
const reflogScanLimit = 50

// isOurPreRewriteCommit reports whether sha is our own earlier form of work
// rather than somebody else's, for a commit git cherry could not match by
// patch-id.
//
// patch-id is not a reliable test of "same change". It ignores hunk line
// numbers but NOT the context lines quoted around a hunk, so rebasing onto a
// base that edited anything within three lines of our change alters the patch-id
// even though the change itself is untouched. That is the ordinary case: a
// branch goes BEHIND, warden's own pre-push rebase step replays it onto the
// updated base, the patch-id moves, and git cherry reports our own pre-rewrite
// commit as work that exists only on the remote. The push is then refused, and
// the advice it prints (`git pull --rebase`) cannot break the cycle because the
// next push rebases again and diverges again. The branch becomes unpushable
// through the gate, which pushes the developer toward `--no-verify` — a gate
// bypass with no provenance, the exact outcome the lease exists to prevent.
//
// Two conditions must BOTH hold, because either alone is too generous:
//
//   - the commit was once reachable from this branch locally (its reflog), so it
//     is a state this branch actually passed through rather than something
//     fetched from the remote; and
//   - it is committed by us, so a colleague's commit that we pulled in and then
//     dropped during an interactive rebase is still protected — the reflog alone
//     would have let that one be discarded.
//
// Anything unreadable — no reflog, no configured identity — answers false and
// the commit stays reported. Refusing to force is always the safe direction.
func (r *Repo) isOurPreRewriteCommit(branch, sha string) bool {
	self, err := r.run("config", "user.email")
	if err != nil || strings.TrimSpace(self) == "" {
		return false
	}
	committer, err := r.run("log", "-1", "--format=%ce", sha)
	if err != nil || !strings.EqualFold(strings.TrimSpace(committer), strings.TrimSpace(self)) {
		return false
	}
	out, err := r.run("reflog", "show", "--format=%H", branch)
	if err != nil {
		return false
	}
	seen := 0
	for line := range strings.SplitSeq(out, "\n") {
		tip := strings.TrimSpace(line)
		if tip == "" {
			continue
		}
		seen++
		if seen > reflogScanLimit {
			break
		}
		if _, err := r.run("merge-base", "--is-ancestor", sha, tip); err == nil {
			return true
		}
	}
	return false
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
	if err == nil {
		return nil
	}
	// A rejected notes push is usually non-fast-forward: something else wrote the
	// ref since this clone last fetched it. That was rare while a developer's
	// machine was the only writer. It is routine now that CI attests merge
	// commits too (#186) — and the losing side's note stayed local forever, so
	// the commit verified on one laptop and read as an ungated bypass everywhere
	// else.
	//
	// Notes are per-object, so the overwhelmingly common case — this machine
	// noted its commit, CI noted a different one — is a clean union with nothing
	// to resolve. Reconcile and retry once.
	return r.mergeAndRetryNotes(remote, err)
}

// notesIncomingRef is the scratch ref the remote's notes are fetched into. It
// lives under refs/notes/ so `git notes merge` will accept it, and is removed
// afterwards.
const notesIncomingRef = "refs/notes/warden-incoming"

// mergeAndRetryNotes reconciles a rejected notes push with the remote ref and
// pushes again.
//
// pushErr — the ORIGINAL rejection — is carried into every error returned here,
// because the recovery's own symptom is rarely the useful half of the story.
func (r *Repo) mergeAndRetryNotes(remote string, pushErr error) error {
	if _, err := r.run("fetch", "--force", remote, NotesRef+":"+notesIncomingRef); err != nil {
		return fmt.Errorf("git: notes push rejected (%v), and the remote ref could not be fetched to reconcile: %w", pushErr, err)
	}
	defer func() { _, _ = r.run("update-ref", "-d", notesIncomingRef) }()

	// git's DEFAULT strategy, on purpose: it unions non-overlapping notes and
	// FAILS on a real conflict — two different attestations of the same commit.
	// Resolving that automatically would mean silently discarding one side's
	// record of a run that actually happened, which is not a decision a git hook
	// should make on someone's behalf.
	if _, err := r.run("notes", "--ref=warden", "merge", notesIncomingRef); err != nil {
		// A failed notes merge leaves a partial worktree state that breaks the
		// next attempt; clear it before returning.
		_, _ = r.run("notes", "--ref=warden", "merge", "--abort")
		return fmt.Errorf("git: notes push rejected (%v), and the remote holds a different note for a "+
			"commit this run also attested — resolve refs/notes/warden by hand: %w", pushErr, err)
	}
	if _, err := r.run("push", "--no-verify", remote, NotesRef+":"+NotesRef); err != nil {
		return fmt.Errorf("git: notes push still rejected after reconciling with the remote: %w", err)
	}
	return nil
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

// TreeSHA returns a commit's tree object SHA — its content fingerprint,
// independent of history or commit metadata. Two commits with the same TreeSHA
// have byte-identical working trees, which is what lets a squash-merge commit be
// re-attested from the already-validated commit it reproduces.
func (r *Repo) TreeSHA(commit string) (string, error) {
	return r.run("rev-parse", commit+"^{tree}")
}

// FileAtRef returns the bytes of the repo-relative path as committed at ref.
// found is false when ref carries no such file (git show exits non-zero), which
// callers treat as "no committed policy there" rather than an error — a bad ref
// surfaces earlier, where the range is resolved. path uses forward slashes,
// matching git's object namespace. This reads committed bytes at a trusted ref
// (a range gate's base), never the working tree it is checking.
func (r *Repo) FileAtRef(ref, path string) (data []byte, found bool, err error) {
	out, err := runRawIn(r.Dir, "show", ref+":"+path)
	if err != nil {
		return nil, false, nil
	}
	return []byte(out), true, nil
}

// NotedCommits returns the commits that carry a refs/notes/warden record — the
// search space a re-attestation draws its already-validated source from.
func (r *Repo) NotedCommits() ([]string, error) {
	out, err := r.run("notes", "--ref", NotesRef, "list")
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range splitLines(out) {
		// each line is "<note-blob-sha> <annotated-commit-sha>"
		if f := strings.Fields(line); len(f) == 2 {
			commits = append(commits, f[1])
		}
	}
	return commits, nil
}

// CommitsInRange returns the SHAs reachable from head back to (but excluding)
// base — the `base..head` set, newest first — for an arbitrary two-endpoint
// range gate (`warden verify --range`). When skipMerges is set, merge commits
// are omitted: a true merge introduces no tree change warden authored and its
// parents are each gated on their own, so requiring a note on the merge itself
// would false-positive.
func (r *Repo) CommitsInRange(base, head string, skipMerges bool) ([]string, error) {
	args := []string{"rev-list"}
	if skipMerges {
		args = append(args, "--no-merges")
	}
	args = append(args, base+".."+head)
	out, err := r.run(args...)
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
