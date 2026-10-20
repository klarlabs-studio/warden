package git

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// TrackedTree writes the tree object of the tracked state of the worktree at
// dir — its index plus every unstaged edit or deletion of a tracked file —
// and returns its name. Untracked files take no part: a pre-commit fix patch
// carries only tracked edits, so this is the tree a commit would get.
//
// The worktree's own index is not touched. A copy of it in a temporary file
// takes the unstaged edits (`git add -u`) and is written with `write-tree`,
// which stores objects in the shared object database and changes no ref,
// index or file.
func TrackedTree(dir string) (string, error) {
	indexPath, err := runIn(dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(dir, indexPath)
	}
	tmpDir, err := os.MkdirTemp("", "warden-index-*")
	if err != nil {
		return "", fmt.Errorf("git: temporary index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpPath := filepath.Join(tmpDir, "index")
	if err := copyIndex(indexPath, tmpPath); err != nil {
		return "", err
	}
	run := func(args ...string) (string, error) {
		cmd := gitCmd(dir, args...)
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+tmpPath)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if _, err := run("add", "-u", "--", "."); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	if !domain.ValidTreeSHA(tree) {
		return "", fmt.Errorf("git: write-tree returned %q", tree)
	}
	return tree, nil
}

// copyIndex copies the index at src to dst. A worktree without an index yet
// copies nothing, and git reads the missing file as an empty index.
func copyIndex(src, dst string) error {
	in, err := os.Open(filepath.Clean(src))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("git: open index: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("git: temporary index: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("git: copy index: %w", err)
	}
	return out.Close()
}
