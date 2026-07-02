// Package sbom collects a lightweight software bill of materials for a validated
// worktree: it finds known dependency lockfiles and content-digests each. The
// digests ride in the (signed, hash-chained) provenance note, so a validated
// commit carries a tamper-evident statement of which dependency sets it had
// (§9). Component-level parsing is intentionally out of scope for now — a
// lockfile digest already pins the exact resolved dependency set.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"go.klarlabs.de/warden/internal/domain"
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

// Collector walks a worktree for known lockfiles. It satisfies application.SBOM.
type Collector struct{}

// New returns an SBOM collector.
func New() *Collector { return &Collector{} }

// Collect returns a digested manifest for every known lockfile under dir, sorted
// by path for a stable record. The .git directory is skipped. A dir it can't
// walk yields an empty SBOM rather than an error — provenance must never fail on
// a best-effort artifact.
func (Collector) Collect(dir string) []domain.DependencyManifest {
	var out []domain.DependencyManifest
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		eco, ok := lockfiles[d.Name()]
		if !ok {
			return nil
		}
		digest, derr := digestFile(path)
		if derr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = d.Name()
		}
		out = append(out, domain.DependencyManifest{
			Ecosystem: eco,
			Path:      filepath.ToSlash(rel),
			Digest:    "sha256:" + digest,
		})
		return nil
	})
	// Deterministic order so the digest set is stable across runs/machines.
	sortManifests(out)
	return out
}

func digestFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
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
