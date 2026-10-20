package feedback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// optionalLocksOff keeps every git call here from taking index.lock
// opportunistically, the same rule the watcher follows.
const optionalLocksOff = "GIT_OPTIONAL_LOCKS=0"

// Objects says where the objects a snapshot computation writes are stored.
type Objects int

const (
	// DiscardObjects writes them to a temporary object directory that is
	// removed afterwards; the repository's own store is only read, as an
	// alternate. Readers use it, so answering an editor never grows the
	// repository however often it asks.
	DiscardObjects Objects = iota
	// KeepObjects writes them to the repository's object store, so a
	// disposable worktree can check the snapshot's tree out. Only the live
	// watcher uses it, once per change it evaluates. Content the store
	// already holds is not written again; what ends up unreferenced is
	// collected by git gc.
	KeepObjects
)

// CurrentSnapshot computes the worktree's snapshot as it is now: the tree
// mode checks, HEAD, the branch, and the given configuration digest. The
// environment digest is recorded as given; it plays no part in the id,
// because a reader never probes a container engine.
//
// For FeedbackModeStaged the tree is the one the index would write. For
// FeedbackModeLive it is the working tree as `git add -A` would stage it:
// tracked files as they are on disk, deleted files gone, untracked files
// included and ignored files excluded.
//
// Both are computed from a private copy of the index (GIT_INDEX_FILE), so the
// computation never writes the worktree's index, never takes its index.lock,
// and cannot fail a commit the developer makes at the same moment. Git
// replaces the index by renaming a complete file over it, so the copy is a
// consistent one.
func CurrentSnapshot(ctx context.Context, dir, mode, configurationDigest, environmentDigest string, objects Objects) (domain.FeedbackSnapshot, error) {
	var stage bool
	switch mode {
	case domain.FeedbackModeStaged:
	case domain.FeedbackModeLive:
		stage = true
	default:
		return domain.FeedbackSnapshot{}, fmt.Errorf("unknown feedback mode %q", mode)
	}
	tree, err := snapshotTree(ctx, dir, stage, objects)
	if err != nil {
		return domain.FeedbackSnapshot{}, err
	}
	// An unborn branch has no HEAD commit; its base is empty.
	base := ""
	if out, err := git(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		base = strings.TrimSpace(string(out))
	}
	branch, err := git(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branch, err = git(ctx, dir, nil, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return domain.FeedbackSnapshot{}, fmt.Errorf("current branch: %w", err)
		}
	}
	return domain.NewFeedbackSnapshot(tree, base, strings.TrimSpace(string(branch)), configurationDigest, environmentDigest), nil
}

// Worktree returns the worktree's absolute top-level directory.
func Worktree(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// snapshotTree writes the tree of a private copy of the index, after staging
// the whole working tree into that copy when stage is set.
func snapshotTree(ctx context.Context, dir string, stage bool, objects Objects) (string, error) {
	index, err := gitPath(ctx, dir, "index")
	if err != nil {
		return "", fmt.Errorf("locate the index: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "warden-feedback-index-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	private := filepath.Join(tmpDir, "index")
	if err := copyIndex(index, private); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("copy the index: %w", err)
	}
	env := []string{"GIT_INDEX_FILE=" + private}
	if objects == DiscardObjects {
		store, err := gitPath(ctx, dir, "objects")
		if err != nil {
			return "", fmt.Errorf("locate the object store: %w", err)
		}
		scratch := filepath.Join(tmpDir, "objects")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return "", err
		}
		alternates := store
		if inherited := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); inherited != "" {
			alternates += string(os.PathListSeparator) + inherited
		}
		env = append(env, "GIT_OBJECT_DIRECTORY="+scratch, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+alternates)
	}
	if stage {
		// --all with no pathspec covers the whole worktree from any
		// subdirectory: modified and deleted tracked files, and untracked
		// files .gitignore does not exclude.
		if _, err := git(ctx, dir, env, "add", "--all"); err != nil {
			return "", fmt.Errorf("stage the working tree: %w", err)
		}
	}
	out, err := git(ctx, dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("compute the snapshot tree: %w", err)
	}
	tree := strings.TrimSpace(string(out))
	if !domain.ValidTreeSHA(tree) {
		return "", fmt.Errorf("compute the snapshot tree: unexpected output %q", tree)
	}
	return tree, nil
}

// gitPath resolves a path inside the git directory (--git-path), absolute.
func gitPath(ctx context.Context, dir, name string) (string, error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path, nil
}

// copyIndex copies the index and keeps its modification time. Git compares
// each entry's recorded mtime with the index file's own to decide which
// entries it cannot trust by stat alone ("racily clean") and must re-read;
// a copy stamped with the time of copying would make git trust entries the
// real index would not, and miss a same-size edit made in the same instant
// the index was written.
func copyIndex(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func git(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), optionalLocksOff), env...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
