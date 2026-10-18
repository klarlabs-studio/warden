// Package sbom collects a lightweight software bill of materials for a validated
// commit: it finds known dependency lockfiles in the commit's tree and
// content-digests each. The digests ride in the (signed, hash-chained)
// provenance note, so a validated commit carries a tamper-evident statement of
// which dependency sets it had (§9). Component-level parsing is intentionally
// out of scope for now — a lockfile digest already pins the exact resolved
// dependency set.
package sbom

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	gitinfra "go.klarlabs.de/warden/internal/infrastructure/git"
)

// lockfiles maps a known lockfile name to its ecosystem. Only these are scanned,
// so the SBOM is deterministic and cheap.
var lockfiles = map[string]string{
	"go.sum":            "go",
	"go.mod":            "go",
	"package-lock.json": "npm",
	"yarn.lock":         "npm",
	"pnpm-lock.yaml":    "npm",
	"Cargo.lock":        "cargo",
	"poetry.lock":       "python",
	"requirements.txt":  "python",
	"Pipfile.lock":      "python",
	"Gemfile.lock":      "rubygems",
	"composer.lock":     "composer",
	"go.work.sum":       "go",
}

// Collector reads known lockfiles out of a commit's tree. It satisfies
// application.SBOM.
type Collector struct{}

// New returns an SBOM collector.
func New() *Collector { return &Collector{} }

// Collect returns a digested manifest for every known lockfile in the tree of
// rev, resolved in the repository at dir, sorted by path for a stable record.
//
// It reads git objects, never the filesystem. The manifest is signed into a
// note that attests rev, so it may state only what rev contains. Walking the
// worktree instead claimed whatever was on disk when the run ended: a lockfile
// a step generated and nobody committed, or a tracked one a step rewrote. Both
// are facts about a directory, not about the commit, and putting them in the
// signed record over-claimed.
//
// Only regular blobs count. A symlink entry's blob is its target path, and a
// submodule entry has no blob here; digesting either would describe something
// other than a lockfile's content.
//
// An empty or unresolvable rev, or a dir that is not a repository, yields an
// empty SBOM rather than an error — provenance must never fail on a
// best-effort artifact, and an empty SBOM claims nothing.
func (Collector) Collect(dir, rev string) []domain.DependencyManifest {
	if rev == "" {
		return nil
	}
	entries, err := lockfileEntries(dir, rev)
	if err != nil || len(entries) == 0 {
		return nil
	}
	contents, err := readBlobs(dir, entries)
	if err != nil {
		return nil
	}
	out := make([]domain.DependencyManifest, 0, len(entries))
	for i, e := range entries {
		sum := sha256.Sum256(contents[i])
		out = append(out, domain.DependencyManifest{
			Ecosystem: lockfiles[path.Base(e.path)],
			Path:      e.path,
			Digest:    "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	// Deterministic order so the digest set is stable across runs/machines.
	sortManifests(out)
	return out
}

// treeEntry is a lockfile blob in the attested tree.
type treeEntry struct {
	object string
	path   string
}

// gitOut runs git in dir with the hook environment scrubbed, so a collector
// invoked from inside a hook resolves against dir rather than the hook's index.
func gitOut(dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitinfra.ScrubHookEnv(cmd.Environ())
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// lockfileEntries lists the regular-file lockfile blobs in rev's tree.
// `ls-tree -z` records are "<mode> <type> <object><TAB><path><NUL>" with the
// path unquoted.
func lockfileEntries(dir, rev string) ([]treeEntry, error) {
	out, err := gitOut(dir, nil, "ls-tree", "-r", "-z", "--full-tree", rev+"^{tree}")
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		if _, known := lockfiles[path.Base(p)]; !known {
			continue
		}
		entries = append(entries, treeEntry{object: fields[2], path: p})
	}
	return entries, nil
}

// readBlobs fetches each entry's content in one `git cat-file --batch`. Output
// is "<object> <type> <size><LF><content><LF>" per request, in request order.
func readBlobs(dir string, entries []treeEntry) ([][]byte, error) {
	var req strings.Builder
	for _, e := range entries {
		req.WriteString(e.object)
		req.WriteByte('\n')
	}
	out, err := gitOut(dir, strings.NewReader(req.String()), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(bytes.NewReader(out))
	contents := make([][]byte, 0, len(entries))
	for _, e := range entries {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("cat-file: truncated output for %s", e.path)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != e.object || fields[1] != "blob" {
			return nil, fmt.Errorf("cat-file: unexpected header %q for %s", strings.TrimSpace(header), e.path)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("cat-file: bad size in %q", strings.TrimSpace(header))
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			return nil, fmt.Errorf("cat-file: short read for %s: %w", e.path, err)
		}
		if _, err := r.Discard(1); err != nil { // the trailing LF
			return nil, fmt.Errorf("cat-file: missing terminator for %s", e.path)
		}
		contents = append(contents, content)
	}
	return contents, nil
}

// sortManifests orders by path (a small, allocation-free insertion sort — the
// lockfile count per repo is tiny).
func sortManifests(m []domain.DependencyManifest) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j].Path < m[j-1].Path; j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

// IsDependencyManifest identifies the manifest names Warden knows how to record.
func IsDependencyManifest(name string) bool { _, ok := lockfiles[name]; return ok }
