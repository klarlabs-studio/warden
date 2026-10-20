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

// CurrentSnapshot computes the worktree's snapshot as it is now: the tree the
// index would write, HEAD, the branch, and the given configuration digest.
// The environment digest is recorded as given; it plays no part in the id,
// because a reader never probes a container engine.
//
// The tree is written from a private copy of the index (GIT_INDEX_FILE), so
// the computation never takes the worktree's index.lock and cannot fail a
// commit the developer makes at the same moment. Git replaces the index by
// renaming a complete file over it, so the copy is a consistent one. Tree
// objects it writes go to the object store, where git gc collects any that
// end up unreferenced.
func CurrentSnapshot(ctx context.Context, dir, configurationDigest, environmentDigest string) (domain.FeedbackSnapshot, error) {
	tree, err := stagedTree(ctx, dir)
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

func stagedTree(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", fmt.Errorf("locate the index: %w", err)
	}
	index := strings.TrimSpace(string(out))
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	tmpDir, err := os.MkdirTemp("", "warden-feedback-index-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	private := filepath.Join(tmpDir, "index")
	if err := copyFile(index, private); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("copy the index: %w", err)
	}
	out, err = git(ctx, dir, []string{"GIT_INDEX_FILE=" + private}, "write-tree")
	if err != nil {
		return "", fmt.Errorf("compute the staged tree: %w", err)
	}
	tree := strings.TrimSpace(string(out))
	if !domain.ValidTreeSHA(tree) {
		return "", fmt.Errorf("compute the staged tree: unexpected output %q", tree)
	}
	return tree, nil
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
