package git

import (
	"fmt"
	"io"
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
func (r *Repo) CreateWorktreeFromHead(materializeDeps bool) (*Worktree, error) {
	// Capture the staged diff (raw — a patch must be byte-exact) before creating
	// the worktree so a failure to stage leaves no orphan directory behind.
	// --binary emits full index lines + base85 hunks so a staged binary file
	// (an image, a built asset) round-trips through the apply below.
	stagedDiff, err := runRawIn(r.Dir, "diff", "--cached", "--binary")
	if err != nil {
		return nil, err
	}

	wt, err := r.addDetachedWorktree("HEAD", materializeDeps)
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
func (r *Repo) CreateWorktreeFromBranch(branch string, materializeDeps bool) (*Worktree, error) {
	return r.addDetachedWorktree(branch, materializeDeps)
}

// addDetachedWorktree creates a temp dir and attaches a detached worktree at
// ref. Detaching avoids leaving a branch checked out in two places, which git
// forbids. materializeDeps controls how gitignored dependency dirs are exposed
// (see exposeGitignoredDeps).
func (r *Repo) addDetachedWorktree(ref string, materializeDeps bool) (*Worktree, error) {
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
	// would fail with "command not found". Expose them from the live checkout so
	// those steps resolve their deps without a slow reinstall. Best-effort: a
	// repo with no node_modules exposes nothing and steps that don't need it are
	// unaffected.
	wt.exposeGitignoredDeps(materializeDeps)
	return wt, nil
}

// depDirNames are gitignored dependency directories worth exposing to steps by
// symlinking them from the live checkout into the worktree.
var depDirNames = map[string]bool{"node_modules": true}

// exposeGitignoredDeps makes each dependency directory (see depDirNames) found
// in the live checkout available in the worktree at the same relative path. It
// never descends into an exposed dependency dir or .git, so a monorepo's nested
// node_modules (web/, apps/*/, site/) are each exposed without walking their
// contents.
//
// The live checkout's dependency dir may itself be a SYMLINK — this is the norm
// in a linked git worktree (e.g. Claude Code's .claude/worktrees/…), where
// node_modules points back at the main checkout's copy so deps aren't installed
// per worktree. Such a symlink is resolved to its real target so the deps are
// still exposed; a naive "only real directories" walk would skip it and leave
// the disposable worktree with no node_modules, failing every JS step.
//
// When materialize is true (the product default — see domain.Config.SymlinkDeps),
// the directory is hardlink-copied so the deps are REAL files inside the worktree
// root, which every tool accepts — including Next.js 16 / Turbopack, which
// rejects a node_modules symlink whose real target resolves outside the
// worktree's filesystem root ("Symlink node_modules is invalid, it points out of
// the filesystem root"). Hardlinks are near-instant on the same filesystem and
// fall back to a byte copy across filesystems. When materialize is false
// (symlink_deps: true) it instead SYMLINKS the resolved directory — fast, O(1),
// and enough for tsc/eslint/vitest and Node's own resolver, which follow it.
func (w *Worktree) exposeGitignoredDeps(materialize bool) {
	root := w.repo.Dir
	// skipDeps stops the walk descending into a real dependency dir (its contents
	// are exposed wholesale); a symlink entry is a leaf and isn't descended anyway.
	skipDeps := func(d fs.DirEntry) error {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort; skip unreadable entries
		}
		name := d.Name()
		if name == ".git" {
			return skipDeps(d)
		}
		if !depDirNames[name] {
			return nil // descend into ordinary dirs; ordinary files are leaves
		}
		// A dependency dir by name — a real directory OR a symlink to one. Resolve
		// symlinks so a worktree that shares node_modules by symlink still works.
		src, err := filepath.EvalSymlinks(path)
		if err != nil {
			return skipDeps(d)
		}
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			return skipDeps(d)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return skipDeps(d)
		}
		target := filepath.Join(w.Dir, rel)
		if _, err := os.Lstat(target); err == nil {
			return skipDeps(d) // already present (tracked, or already exposed)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return skipDeps(d)
		}
		if materialize {
			if err := materializeTree(src, target); err != nil {
				// Best-effort: fall back to a symlink so the run still proceeds
				// rather than leaving a half-populated dependency dir behind.
				_ = os.RemoveAll(target)
				_ = os.Symlink(src, target)
			}
		} else {
			_ = os.Symlink(src, target)
		}
		return skipDeps(d)
	})
}

// materializeTree recreates the directory tree rooted at src under dst using
// hardlinks for regular files (near-instant, no extra disk on the same
// filesystem), falling back to a byte copy when a hardlink can't be made (e.g.
// src and dst live on different filesystems). Directories are recreated and
// symlinks are preserved verbatim, so the result is a real in-root directory a
// filesystem-root-strict tool (Turbopack) accepts.
func materializeTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, out)
		case d.Type().IsRegular():
			// Hardlink shares the inode (fast); on a cross-filesystem link error,
			// fall back to copying the bytes so materialization still succeeds.
			if err := os.Link(path, out); err == nil {
				return nil
			}
			return copyFile(path, out)
		default:
			return nil // skip sockets/devices/pipes — never present in node_modules
		}
	})
}

// copyFile copies src to dst preserving the file mode, used as the cross-
// filesystem fallback for materializeTree.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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
	// The per-worktree golangci-lint cache (see steps.stepEnv) is a sibling
	// derived from the worktree dir; remove it too so runs don't leak caches.
	_ = os.RemoveAll(w.Dir + "-golangci-cache")
	if gitErr != nil {
		return gitErr
	}
	if rmErr != nil {
		return fmt.Errorf("git: remove worktree dir: %w", rmErr)
	}
	return nil
}
