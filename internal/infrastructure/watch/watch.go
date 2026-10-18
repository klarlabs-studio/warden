// Package watch provides the change-detection primitives behind `warden watch`:
// fingerprints of the working tree and of the staging area, and the coalescing
// loop both modes share.
package watch

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// skipDirs are directories never worth watching — noise that would trigger
// spurious re-runs (VCS metadata, dependency and build output).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, "dist": true, "build": true, ".venv": true,
}

// maxHashBytes caps the files whose content is hashed; larger files are
// identified by size and mtime alone.
const maxHashBytes = 4 << 20

// racyWindow is how long after a file was hashed its mtime still cannot be
// trusted to reveal a same-size rewrite (coarse filesystem timestamps). Within
// it the file is re-hashed on every sample.
const racyWindow = 2 * time.Second

type fileState struct {
	size     int64
	mod      time.Time
	ino      uint64
	sum      [sha256.Size]byte
	hashed   bool
	hashedAt time.Time
}

// Tree fingerprints a working tree by content. Content, not mtime, so a check
// that rewrites a file with identical bytes (coverage output, a formatter with
// nothing to do) does not retrigger itself forever. File stats gate the
// hashing: only files whose size, mtime or inode changed, or that are still
// inside the racy window, are read again. A Tree is not safe for concurrent
// use.
type Tree struct {
	dir   string
	files map[string]fileState
}

// NewTree returns a fingerprinter for dir.
func NewTree(dir string) *Tree { return &Tree{dir: dir, files: map[string]fileState{}} }

// Fingerprint returns a digest of the tree's files by path and content. It
// changes iff a file is added, removed, or its content changes. Unreadable
// entries are skipped so a transient error never wedges the loop. Directories
// holding a `.git` file (nested linked worktrees, submodules) are separate
// checkouts and are not walked.
func (t *Tree) Fingerprint() string {
	type entry struct {
		path  string
		state fileState
	}
	var entries []entry
	next := make(map[string]fileState, len(t.files))
	_ = filepath.WalkDir(t.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (path != t.dir && hasGitFile(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(t.dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		state := t.observe(path, rel, info)
		next[rel] = state
		entries = append(entries, entry{rel, state})
		return nil
	})
	t.files = next
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	h := sha256.New()
	buf := make([]byte, 8)
	for _, e := range entries {
		h.Write([]byte(e.path))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(buf, uint64(e.state.size))
		h.Write(buf)
		if e.state.hashed {
			h.Write(e.state.sum[:])
		} else {
			binary.LittleEndian.PutUint64(buf, uint64(e.state.mod.UnixNano()))
			h.Write(buf)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// observe returns path's state, reusing the cached hash when the stats match
// and the file has left the racy window.
func (t *Tree) observe(path, rel string, info fs.FileInfo) fileState {
	state := fileState{size: info.Size(), mod: info.ModTime(), ino: inode(info)}
	prev, ok := t.files[rel]
	if ok && prev.hashed && prev.size == state.size && prev.mod.Equal(state.mod) && prev.ino == state.ino &&
		state.mod.Before(prev.hashedAt.Add(-racyWindow)) {
		return prev
	}
	if !info.Mode().IsRegular() || state.size > maxHashBytes {
		return state
	}
	now := time.Now()
	if sum, err := hashFile(path); err == nil {
		state.sum, state.hashed, state.hashedAt = sum, true, now
	}
	return state
}

func hashFile(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxHashBytes+1)); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// hasGitFile reports whether dir contains a `.git` FILE — the marker of a
// linked worktree or submodule checkout nested inside the watched tree.
func hasGitFile(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && !info.IsDir()
}
