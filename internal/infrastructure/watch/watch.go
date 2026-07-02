// Package watch provides the change-detection primitive behind `warden watch`:
// a cheap, dependency-free fingerprint of a working tree that changes whenever a
// tracked file is added, removed, or modified. The command polls it to re-run
// the fast checks on save — a continuous pre-commit feedback loop.
package watch

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"sort"
)

// skipDirs are directories never worth watching — noise that would trigger
// spurious re-runs (VCS metadata, dependency and build output).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, "dist": true, "build": true, ".venv": true,
}

// Fingerprint returns a digest of dir's files by path, size, and mtime. It
// changes iff a file is added, removed, resized, or touched — enough to drive a
// watch loop without reading file contents. Unreadable entries are skipped so a
// transient error never wedges the loop.
func Fingerprint(dir string) string {
	type entry struct {
		path string
		size int64
		mod  int64
	}
	var entries []entry
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		entries = append(entries, entry{filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	h := sha256.New()
	buf := make([]byte, 8)
	for _, e := range entries {
		h.Write([]byte(e.path))
		binary.LittleEndian.PutUint64(buf, uint64(e.size))
		h.Write(buf)
		binary.LittleEndian.PutUint64(buf, uint64(e.mod))
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))
}
