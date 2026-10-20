// Package sbom collects a lightweight software bill of materials for a validated
// commit: it finds known dependency lockfiles in the commit's tree and
// content-digests each. The digests ride in the (signed, hash-chained)
// provenance note, so a validated commit carries a tamper-evident statement of
// which dependency sets it had (§9). Component-level parsing is intentionally
// out of scope for now — a lockfile digest already pins the exact resolved
// dependency set.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"path"

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
	blobs, err := gitinfra.ReadTreeBlobs(dir, rev, isLockfilePath)
	if err != nil || len(blobs) == 0 {
		return nil
	}
	out := make([]domain.DependencyManifest, 0, len(blobs))
	for _, b := range blobs {
		sum := sha256.Sum256(b.Content)
		out = append(out, domain.DependencyManifest{
			Ecosystem: lockfiles[path.Base(b.Path)],
			Path:      b.Path,
			Digest:    "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	// Deterministic order so the digest set is stable across runs/machines.
	sortManifests(out)
	return out
}

// isLockfilePath reports whether a repository path names a known lockfile.
func isLockfilePath(p string) bool { _, ok := lockfiles[path.Base(p)]; return ok }

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
