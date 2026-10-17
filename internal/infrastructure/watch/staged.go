package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StagedFingerprint reads the index itself, so staging changes trigger checks
// even when the working file's size and modification time do not change.
// Git resolves the worktree-specific index (including linked worktrees).
func StagedFingerprint(ctx context.Context, dir string) (string, error) {
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	location, err := git("rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(location))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read staged index: %w", err)
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
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

// RunStaged coalesces changes until two samples agree. Obsolete work is
// cancelled immediately; the replacement waits for teardown, so evaluations
// never overlap. report is called only for current completed evaluations.
func RunStaged(ctx context.Context, interval time.Duration, fingerprint func(context.Context) (string, error), evaluate func(context.Context) error, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last, completed, running, lastError string
	var runContext context.Context
	var cancel context.CancelFunc
	var done chan error
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	sample := func() {
		value, err := fingerprint(ctx)
		if err != nil {
			if report != nil && ctx.Err() == nil && lastError != err.Error() {
				report(err)
			}
			lastError = err.Error()
			last = ""
			if cancel != nil {
				cancel()
			}
			return
		}
		lastError = ""
		if value != last {
			last = value
			if cancel != nil && running != value {
				cancel()
			}
			return
		}
		if done == nil && value != "" && value != completed {
			child, stop := context.WithCancel(ctx)
			runContext = child
			cancel = stop
			running = value
			done = make(chan error, 1)
			go func() { done <- evaluate(child) }()
		}
	}
	sample()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-done:
			current := runContext.Err() == nil
			cancel()
			cancel = nil
			done = nil
			if current && running == last && last != "" {
				completed = running
				if report != nil {
					report(err)
				}
			}
		case <-ticker.C:
			sample()
		}
	}
}
