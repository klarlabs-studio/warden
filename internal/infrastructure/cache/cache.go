// Package cache implements warden's step-level cache: a step whose declared
// input files are byte-identical to its last passing run is skipped. The store
// is a JSON file under the repo's git dir (per-clone, never committed), and the
// fingerprint is a content hash of the matched files, so any change to a
// declared input invalidates the entry (§4.4).
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// FileCache is a concurrency-safe step cache backed by a JSON file. Keys map to
// a sentinel (true); presence means "passed with these inputs".
type FileCache struct {
	path string
	mu   sync.Mutex
	keys map[string]bool
}

// Open loads (or starts) the cache under gitDir. A missing or corrupt file
// yields an empty cache rather than an error — the cache is an optimization, so
// it must never block a run.
func Open(gitDir string) *FileCache {
	c := &FileCache{path: filepath.Join(gitDir, "warden-step-cache.json"), keys: map[string]bool{}}
	if raw, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(raw, &c.keys)
	}
	return c
}

// Seen reports whether key was recorded before.
func (c *FileCache) Seen(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys[key]
}

// Record marks key as passed and persists the store. Best-effort: a write
// failure is ignored, since a lost cache entry only costs a re-run.
func (c *FileCache) Record(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[key] = true
	raw, err := json.Marshal(c.keys)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path, raw, 0o600)
}

// Fingerprint hashes the contents of the files under dir that match any glob,
// in sorted path order, so the digest is stable and changes iff a declared
// input changes. It returns "" when no file matches (nothing to cache on).
func (c *FileCache) Fingerprint(dir string, globs []string) string {
	matches := matchFiles(dir, globs)
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	h := sha256.New()
	for _, rel := range matches {
		content, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "" // an unreadable declared input: don't risk a false hit
		}
		// Include the path and a length delimiter so distinct files can't collide
		// by content concatenation.
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(content)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// matchFiles walks dir and returns the repo-relative paths of regular files
// matching any glob. The .git directory is skipped.
func matchFiles(dir string, globs []string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, g := range globs {
			if matchGlob(g, rel) {
				out = append(out, rel)
				return nil
			}
		}
		return nil
	})
	return out
}

// matchGlob reports whether path matches a glob supporting `**` (any run of
// characters including slashes), `*` (any run within a path segment), and `?`
// (one non-slash character). Matching is anchored to the full relative path.
func matchGlob(glob, path string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++ // consume the second star
				// Swallow a following slash so `**/x` also matches `x` at the root.
				if i+1 < len(glob) && glob[i+1] == '/' {
					b.WriteString("/?")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteByte(glob[i])
		default:
			b.WriteByte(glob[i])
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(path)
}
