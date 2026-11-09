package application

import "path/filepath"

// buildCacheDir resolves the per-clone directory warden roots build caches at.
//
// Best-effort: a repo whose git dir cannot be read simply gets no cache reuse,
// which costs speed and nothing else. It must never fail a run — the gate's
// verdict cannot depend on whether an optimization was available.
func (r *Runner) buildCacheDir() string {
	gitDir, err := r.Git.GitDir()
	if err != nil || gitDir == "" {
		return ""
	}
	return BuildCacheDir(gitDir)
}

// BuildCacheDir is the build-cache directory of the clone whose git dir is
// gitDir. Every surface that reads, bounds or removes the cache resolves it
// here, so they cannot disagree about where it is.
func BuildCacheDir(gitDir string) string {
	return filepath.Join(gitDir, "warden", "build-cache")
}

// holdBuildCache takes the run's shared hold on the build cache, for as long
// as its steps run. A cache that cannot be held is not used: sc loses its
// BuildCacheDir, so the steps build cold rather than in a directory a
// concurrent eviction could remove underneath them. That costs speed, never
// the run.
func (r *Runner) holdBuildCache(sc *StepContext) (release func()) {
	if r.BuildCache == nil || sc.BuildCacheDir == "" {
		return func() {}
	}
	release, err := r.BuildCache.Share(sc.BuildCacheDir)
	if err != nil {
		sc.BuildCacheDir = ""
		return func() {}
	}
	return release
}

// boundBuildCache trims the build cache once a run is over — after its steps,
// its push and its note, so the measurement is never in a step's critical
// path. It is deferred by the public entry points and reads nothing of the
// run: the verdict is already decided and cannot depend on whether eviction
// happened, was skipped because the cache was busy, or failed.
func (r *Runner) boundBuildCache() {
	if r.BuildCache == nil {
		return
	}
	if dir := r.buildCacheDir(); dir != "" {
		_ = r.BuildCache.Trim(dir)
	}
}
