package git

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Worktree is a disposable git worktree Warden validates in, isolated from the
// developer's working tree so a run never disturbs uncommitted work (§4.2).
// Remove tears it down.
type Worktree struct {
	Dir  string
	repo *Repo
}

// CreateWorktreeFromHead makes a detached worktree at HEAD with the currently
// staged changes applied on top, reproducing exactly what a pre-commit would
// produce. Seeding the index from a staged diff lets steps see the pending
// change without touching the real working tree (§4.2).
func (r *Repo) CreateWorktreeFromHead() (*Worktree, error) {
	// Capture the staged diff (raw — a patch must be byte-exact) before creating
	// the worktree so a failure to stage leaves no orphan directory behind.
	// --binary emits full index lines + base85 hunks so a staged binary file
	// (an image, a built asset) round-trips through the apply below.
	stagedDiff, err := runRawIn(r.Dir, "diff", "--cached", "--binary")
	if err != nil {
		return nil, err
	}

	wt, err := r.addDetachedWorktree("HEAD")
	if err != nil {
		return nil, err
	}

	// Empty diff (nothing staged) needs no apply; git apply on empty input
	// would error.
	if stagedDiff != "" {
		if err := wt.applyAndStage(stagedDiff); err != nil {
			_ = wt.Remove()
			return nil, err
		}
	}
	return wt, nil
}

// CreateWorktreeFromBranch makes a detached worktree at the tip of branch, the
// clean starting point for a pre-push run where the changes are already
// committed (§4.3).
func (r *Repo) CreateWorktreeFromBranch(branch string) (*Worktree, error) {
	return r.addDetachedWorktree(branch)
}

// addDetachedWorktree creates a temp dir and attaches a detached worktree at
// ref. Detaching avoids leaving a branch checked out in two places, which git
// forbids.
func (r *Repo) addDetachedWorktree(ref string) (*Worktree, error) {
	dir, err := os.MkdirTemp("", "warden-wt-")
	if err != nil {
		return nil, fmt.Errorf("git: create worktree temp dir: %w", err)
	}
	if _, err := r.run("worktree", "add", "--detach", dir, ref); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	wt := &Worktree{Dir: dir, repo: r}
	// A git worktree only contains tracked files, so gitignored dependency
	// directories (node_modules) are absent — JS/TS steps (tsc, eslint, vitest)
	// would fail with "command not found". Symlink them in from the live
	// checkout so those steps resolve their deps without a slow reinstall.
	// Best-effort: a repo with no node_modules links nothing and steps that
	// don't need it are unaffected.
	wt.linkGitignoredDeps()
	return wt, nil
}

// depDirNames are gitignored dependency directories worth exposing to steps by
// symlinking them from the live checkout into the worktree.
var depDirNames = map[string]bool{"node_modules": true}

// linkGitignoredDeps symlinks each dependency directory (see depDirNames) found
// in the live checkout into the worktree at the same relative path. It never
// descends into a linked dependency dir or .git, so a monorepo's nested
// node_modules (web/, apps/*/, site/) are each linked without walking their
// contents.
func (w *Worktree) linkGitignoredDeps() {
	root := w.repo.Dir
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // best-effort; skip unreadable entries
		}
		name := d.Name()
		if name == ".git" {
			return filepath.SkipDir
		}
		if !depDirNames[name] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return filepath.SkipDir
		}
		target := filepath.Join(w.Dir, rel)
		if _, err := os.Lstat(target); err == nil {
			return filepath.SkipDir // already present (tracked, or already linked)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
			_ = os.Symlink(path, target)
		}
		return filepath.SkipDir // never walk into the dependency dir itself
	})
}

// applyAndStage applies a unified diff to the worktree and stages it, so the
// seeded change is committable exactly as the developer staged it.
func (w *Worktree) applyAndStage(diff string) error {
	// run() trims trailing whitespace, but git apply rejects a patch whose
	// final hunk lacks a terminating newline, so restore it here.
	if !strings.HasSuffix(diff, "\n") {
		diff += "\n"
	}
	cmd := gitCmd(w.Dir, "apply", "--index", "--binary", "--whitespace=nowarn", "-")
	cmd.Stdin = strings.NewReader(diff)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git apply (seed worktree): %w: %s", err, string(out))
	}
	return nil
}

// Remove detaches the worktree via git and deletes its temp dir. It uses
// --force because Warden's steps may leave build artifacts that would otherwise
// HeadSHA is the worktree's current commit, read after steps (rebase, fix
// commits) have run so the pre-push fast-forward targets the validated tip.
func (w *Worktree) HeadSHA() (string, error) {
	return runIn(w.Dir, "rev-parse", "HEAD")
}

// DiffSince returns the worktree's unstaged modifications — exactly the edits
// an auto-fixing step made on top of the seeded (already-staged) state, so the
// pre-commit hook can re-apply just the fixes to the developer's live tree
// without re-touching what they had already staged (§4.2).
func (w *Worktree) DiffSince() (string, error) {
	// Raw — the returned patch is re-applied to the live tree byte-for-byte;
	// --binary so an auto-fix that touches a binary file re-applies cleanly.
	return runRawIn(w.Dir, "diff", "--binary")
}

// block removal.
func (w *Worktree) Remove() error {
	_, gitErr := w.repo.run("worktree", "remove", "--force", w.Dir)
	// Always attempt directory cleanup even if git already pruned the entry, so
	// a partially-created worktree never leaks disk.
	rmErr := os.RemoveAll(w.Dir)
	if gitErr != nil {
		return gitErr
	}
	if rmErr != nil {
		return fmt.Errorf("git: remove worktree dir: %w", rmErr)
	}
	return nil
}
