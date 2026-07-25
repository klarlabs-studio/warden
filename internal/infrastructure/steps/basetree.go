package steps

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// fallbackBaseRefs are the last-resort refs for a branch with no upstream: the
// remote's default branch is where the change is headed even if nothing tracks
// it yet.
var fallbackBaseRefs = []string{"origin/HEAD", "origin/main", "origin/master"}

// errNoBase means warden could not work out what to compare against.
var errNoBase = errors.New("no base ref")

// resolveBaseSHA finds the commit the change under review should be measured
// against: the merge-base of HEAD and the branch's upstream. An explicit
// configured ref wins, and is resolved strictly — a typo there must surface as
// an error, not silently fall back to a different base and change what the gate
// enforces.
//
// branch is named explicitly rather than read from HEAD because warden validates
// in a DETACHED worktree, where `@{upstream}` resolves to nothing. Asking for
// `<branch>@{upstream}` gets the tracking ref out of the shared config, which is
// the honest answer to "what does this push add"; the bare `@{upstream}` and the
// remote default branch remain as fallbacks.
func resolveBaseSHA(ctx context.Context, dir, branch, configured string) (string, error) {
	if configured != "" {
		sha, err := gitOut(ctx, dir, "merge-base", "HEAD", configured)
		if err != nil {
			return "", fmt.Errorf("security_scan.base %q: %s", configured, strings.TrimSpace(sha))
		}
		return strings.TrimSpace(sha), nil
	}

	var candidates []string
	if branch != "" {
		if up := upstreamOf(ctx, dir, branch+"@{upstream}"); up != "" {
			candidates = append(candidates, up)
		}
	}
	if up := upstreamOf(ctx, dir, "@{upstream}"); up != "" {
		candidates = append(candidates, up)
	}
	if branch != "" {
		candidates = append(candidates, "origin/"+branch)
	}
	candidates = append(candidates, fallbackBaseRefs...)

	for _, ref := range candidates {
		if sha, err := gitOut(ctx, dir, "merge-base", "HEAD", ref); err == nil {
			if s := strings.TrimSpace(sha); s != "" {
				return s, nil
			}
		}
	}
	return "", errNoBase
}

// upstreamOf resolves a tracking ref to its symbolic name, or "" when there is
// none.
func upstreamOf(ctx context.Context, dir, spec string) string {
	out, err := gitOut(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", spec)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// maxBaseTreeBytes caps how much of a base tree warden will materialize. A
// scanner needs source, not a game asset directory; refusing past this keeps a
// git hook from filling the disk on a repo with a large committed blob.
const maxBaseTreeBytes = 2 << 30

// materializeTree extracts the tree of commit sha into dest.
//
// It goes through `git archive` and a tar reader rather than `git worktree add`
// on purpose: the base scan is a read-only side errand inside an already-running
// hook, and adding a second registered worktree would take the repo's worktree
// lock, leave state behind if the hook is killed, and collide with the
// disposable worktree the run is already using. An archive stream touches
// nothing but the destination directory.
func materializeTree(ctx context.Context, repoDir, sha, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "archive", "--format=tar", sha)
	cmd.Dir = repoDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(stdout, dest)
	// Drain anything the extractor did not read so git never blocks on a full
	// pipe while we wait for it.
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %v: %s", sha, err, strings.TrimSpace(stderr.String()))
	}
	return extractErr
}

// extractTar writes a tar stream into dest, keeping only regular files and
// directories. Symlinks, devices and hard links are skipped: they carry no
// scannable content, and materializing a link out of an archive is how an
// extractor gets talked into writing outside its destination.
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read base tree: %w", err)
		}
		target, ok := safeJoin(dest, hdr.Name)
		if !ok {
			return fmt.Errorf("base tree entry %q escapes the destination", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			written += hdr.Size
			if written > maxBaseTreeBytes {
				return fmt.Errorf("base tree exceeds %d bytes", int64(maxBaseTreeBytes))
			}
			if err := writeTarFile(tr, target, hdr.Size); err != nil {
				return err
			}
		default:
			// Skipped by design; see the doc comment.
		}
	}
}

func writeTarFile(tr *tar.Reader, target string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.CopyN(f, tr, size); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return f.Close()
}

// safeJoin resolves a tar entry name inside dest, rejecting anything that
// climbs out of it.
func safeJoin(dest, name string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(dest, clean), true
}
