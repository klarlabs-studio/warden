package git

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// lockPackages is the subset of a lockfile we compare: the resolved version of
// every package, keyed by its node_modules path.
type lockPackages struct {
	Packages map[string]struct {
		Version string `json:"version"`
	} `json:"packages"`
}

// DetectDepDrift compares each package-lock.json committed at rev against what
// is actually installed in the live checkout. The lockfiles are read from
// rev's tree in the repository at dir, never from the worktree's files.
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
//
// The result is signed into a note that attests rev, so it may speak only of
// lockfiles rev contains. Walking the worktree instead reported drift for a
// lockfile a step generated and nobody committed, and compared against a
// committed lockfile as a step had rewritten it — which hid exactly the drift
// this exists to catch when the rewrite matched the install. An empty or
// unresolvable rev yields nothing: no commit, no claim.
func DetectDepDrift(dir, rev, liveDir string) []domain.DepDrift {
	if rev == "" {
		return nil
	}
	blobs, err := ReadTreeBlobs(dir, rev, isProjectPackageLock)
	if err != nil {
		return nil
	}
	var drifts []domain.DepDrift

	for _, blob := range blobs {
		lockRel := blob.Path
		locked, err := parseLockPackages(blob.Content)
		if err != nil || len(locked) == 0 {
			continue
		}

		// The hidden lockfile sits beside the install it describes, in the
		// LIVE checkout — that is the whole point: it says what is on the
		// machine, not what the commit asked for.
		hidden := filepath.Join(liveDir, filepath.FromSlash(path.Dir(lockRel)), "node_modules", ".package-lock.json")
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
func readLockPackages(file string) (map[string]string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- paths are derived from the worktree/checkout roots
	if err != nil {
		return nil, err
	}
	return parseLockPackages(b)
}

// parseLockPackages decodes package-lock.json content into a path → version
// map.
func parseLockPackages(b []byte) (map[string]string, error) {
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

// isProjectPackageLock reports whether a repository path is one of the
// project's own package-lock.json files: not .git's, and not one shipped
// inside a dependency directory, which describes that dependency's tree and
// would report drift for packages the project never declared.
func isProjectPackageLock(p string) bool {
	if path.Base(p) != "package-lock.json" {
		return false
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if seg == ".git" || depDirNames[seg] {
			return false
		}
	}
	return true
}
