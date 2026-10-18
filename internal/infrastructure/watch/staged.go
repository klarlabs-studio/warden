package watch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OptionalLocksOff is the environment entry every watcher-originated git call
// carries, so a background `git` never takes index.lock opportunistically
// (index refresh) and races the developer's own commit.
const OptionalLocksOff = "GIT_OPTIONAL_LOCKS=0"

// DefaultBackstop bounds how long a stat-gated fingerprint may be reused
// without a full recomputation, covering a write whose size and mtime match
// the previous one (racy mtime).
const DefaultBackstop = 5 * time.Second

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), OptionalLocksOff)
	return cmd.Output()
}

func gitPath(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutput(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path, nil
}

// StagedFingerprint reads the index itself, so staging changes trigger checks
// even when the working file's size and modification time do not change.
// Git resolves the worktree-specific index (including linked worktrees).
func StagedFingerprint(ctx context.Context, dir string) (string, error) {
	path, err := gitPath(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read staged index: %w", err)
	}
	head, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	branch, err := gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(data)
	h.Write([]byte{0})
	h.Write(head)
	h.Write([]byte{0})
	h.Write(branch)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NothingStaged reports whether the index matches HEAD. It answers true only
// when git says so (`diff --cached --quiet` exits 0); any other failure is an
// error, never a claim that there is nothing to check.
func NothingStaged(ctx context.Context, dir string) (bool, error) {
	_, err := gitOutput(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect staged changes: %w", err)
}

// StatGate memoises an expensive fingerprint behind a cheap key (file stats).
// The expensive value is recomputed when the key changes, and at least every
// Backstop regardless, so a write that leaves size and mtime unchanged is still
// seen within Backstop.
type StatGate struct {
	Key      func(context.Context) (string, error)
	Full     func(context.Context) (string, error)
	Backstop time.Duration

	mu    sync.Mutex
	key   string
	value string
	at    time.Time
}

// Fingerprint returns the memoised value, recomputing it when needed.
func (g *StatGate) Fingerprint(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key, keyErr := g.Key(ctx)
	backstop := g.Backstop
	if backstop <= 0 {
		backstop = DefaultBackstop
	}
	if keyErr == nil && g.value != "" && key == g.key && time.Since(g.at) < backstop {
		return g.value, nil
	}
	value, err := g.Full(ctx)
	if err != nil {
		g.value = ""
		return "", err
	}
	if keyErr != nil {
		key = ""
	}
	g.key, g.value, g.at = key, value, time.Now()
	return value, nil
}

// StagedStatKey returns a cheap key over the inputs StagedFingerprint reads:
// the index, HEAD, the ref HEAD points at, packed-refs, and the extra files
// given (configuration; relative paths resolve against the worktree root). Git is asked for the paths once; each call afterwards
// is a handful of stats and one small read.
func StagedStatKey(ctx context.Context, dir string, extra ...string) (func(context.Context) (string, error), error) {
	index, err := gitPath(ctx, dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	head, err := gitPath(ctx, dir, "rev-parse", "--git-path", "HEAD")
	if err != nil {
		return nil, err
	}
	common, err := gitPath(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	top, err := gitPath(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	files := []string{index, head, filepath.Join(common, "packed-refs")}
	for _, p := range extra {
		if !filepath.IsAbs(p) {
			p = filepath.Join(top, p)
		}
		files = append(files, p)
	}
	return func(context.Context) (string, error) {
		var b bytes.Buffer
		headData, err := os.ReadFile(head)
		if err != nil {
			return "", err
		}
		b.Write(headData)
		paths := append([]string(nil), files...)
		if ref, ok := strings.CutPrefix(strings.TrimSpace(string(headData)), "ref: "); ok {
			paths = append(paths, filepath.Join(common, filepath.FromSlash(ref)))
		}
		for _, p := range paths {
			b.WriteString(p)
			b.WriteByte(0)
			b.WriteString(statKey(p))
			b.WriteByte(0)
		}
		return b.String(), nil
	}, nil
}

// statKey is a file's identity for change detection: size, mtime and inode
// (where the platform has one). A missing file has a stable key of its own.
func statKey(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), inode(info))
}
