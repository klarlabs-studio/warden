package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.klarlabs.de/warden/internal/domain"
)

// lockPackages is the subset of a lockfile we compare: the resolved version of
// every package, keyed by its node_modules path.
type lockPackages struct {
	Packages map[string]struct {
		Version string `json:"version"`
	} `json:"packages"`
}

// DetectDepDrift compares each committed lockfile in the worktree against what
// is actually installed in the live checkout.
//
// Warden exposes node_modules from the developer's checkout rather than
// reinstalling, because a per-run `npm ci` is the dominant cost on a large JS
// repo. The consequence is that the tracked tree comes from the commit and the
// dependency tree comes from the machine: after a branch switch without a
// reinstall, a local `npm link`, or a half-applied upgrade, the checks run
// against dependencies the commit does not specify — and CI, installing fresh
// from the lockfile, can legitimately disagree (#204).
//
// The comparison is cheap and needs no install: npm maintains
// node_modules/.package-lock.json as a manifest of what it actually put on
// disk, so this is two file reads and a map diff per lockfile.
//
// Best-effort by design. A repo with no lockfile, no install, or a package
// manager that writes no hidden lockfile (yarn, pnpm) reports no drift rather
// than a false alarm — silence here means "nothing detected", not "verified
// clean", and the caller must not imply otherwise.
func DetectDepDrift(worktreeDir, liveDir string) []domain.DepDrift {
	var drifts []domain.DepDrift

	for _, lockRel := range findLockfiles(worktreeDir) {
		locked, err := readLockPackages(filepath.Join(worktreeDir, lockRel))
		if err != nil || len(locked) == 0 {
			continue
		}

		// The hidden lockfile sits beside the install it describes, in the
		// LIVE checkout — that is the whole point: it says what is on the
		// machine, not what the commit asked for.
		hidden := filepath.Join(liveDir, filepath.Dir(lockRel), "node_modules", ".package-lock.json")
		installed, err := readLockPackages(hidden)
		if err != nil || len(installed) == 0 {
			continue
		}

		d := domain.DepDrift{Lockfile: lockRel}
		for pkgPath, want := range locked {
			// Skip the root project entry ("") and anything without a pinned
			// version: workspace links and file: deps have no version to
			// compare and are not drift.
			if pkgPath == "" || want == "" {
				continue
			}
			got, ok := installed[pkgPath]
			switch {
			case !ok:
				d.Missing = append(d.Missing, pkgPath)
			case got != want:
				d.Mismatched = append(d.Mismatched, fmt.Sprintf("%s: %s != %s", pkgPath, got, want))
			}
		}

		if len(d.Missing) > 0 || len(d.Mismatched) > 0 {
			sort.Strings(d.Missing)
			sort.Strings(d.Mismatched)
			drifts = append(drifts, d)
		}
	}

	sort.Slice(drifts, func(i, j int) bool { return drifts[i].Lockfile < drifts[j].Lockfile })
	return drifts
}

// readLockPackages reads a package-lock.json (or the hidden
// node_modules/.package-lock.json, which shares its shape) into a
// path → version map.
func readLockPackages(path string) (map[string]string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- paths are derived from the worktree/checkout roots
	if err != nil {
		return nil, err
	}
	var lp lockPackages
	if err := json.Unmarshal(b, &lp); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(lp.Packages))
	for k, v := range lp.Packages {
		out[k] = v.Version
	}
	return out, nil
}

// findLockfiles returns repo-relative paths of every package-lock.json in the
// worktree, skipping dependency directories and .git so a monorepo's nested
// lockfiles are found without walking their installs.
func findLockfiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is not drift
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || depDirNames[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "package-lock.json" {
			if rel, err := filepath.Rel(root, path); err == nil {
				out = append(out, rel)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}
