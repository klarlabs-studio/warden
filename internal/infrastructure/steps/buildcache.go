package steps

import (
	"os"
	"path/filepath"
	"strings"
)

// Build-cache reuse across runs.
//
// A git worktree contains TRACKED FILES ONLY, which is what makes warden's
// isolation worth having — a run cannot be contaminated by whatever is lying
// around in the developer's checkout. But it also means every gitignored build
// cache is absent, so a compiled language rebuilds from scratch on every single
// gated push.
//
// Measured on a six-crate Rust workspace: a cold `cargo check` in a fresh
// worktree took 36s where the warm one took 1.9s, and that repo's pre-push also
// runs `cargo test` and `clippy --all-targets` — each in its OWN worktree,
// because independent steps run in parallel. The gate was several minutes of
// recompilation per push. It had produced ZERO provenance notes: nobody had ever
// waited for it, so the gate was installed and 100% routed around.
//
// Dependency directories were already solved by hardlink-copying node_modules
// into the worktree. A build cache is a different problem and must not be solved
// the same way: node_modules is read-mostly, whereas a compiler WRITES to its
// cache, and hardlinks share inodes — so a gated build could corrupt the
// developer's live cache. Instead the cache is redirected, by pointing each
// toolchain's own cache-location variable at a warden-owned directory that
// persists across runs. The toolchain's own locking then handles concurrency,
// which is exactly what those variables exist for.
//
// The cache lives under .git, so it is per-clone, never committed, and removed
// with the clone.

// buildCache is one toolchain's cache redirection: the env var that relocates
// it, and how to tell the toolchain is in use.
type buildCache struct {
	// env is the variable the toolchain reads for its cache location.
	env string
	// dir is the subdirectory under the warden cache root. Named per toolchain
	// so two toolchains in one repo never share a directory.
	dir string
	// marker is a file whose presence at the worktree root means this toolchain
	// builds here. Detection is deliberately by MARKER rather than by trying
	// every variable: setting a cache variable for a toolchain the repo does not
	// use would create empty directories and, worse, make warden look like the
	// cause if that toolchain later behaved oddly.
	marker string
}

// buildCaches are the toolchains warden redirects.
//
// Deliberately short. Each entry is a claim that redirecting this variable is
// both safe and worth it, and the only way to know is to measure — so a
// toolchain gets added when someone has, not because the variable exists.
//
// Go is absent ON PURPOSE: its build cache already lives outside the repo
// (GOCACHE defaults under the user cache dir), so a fresh worktree is already
// warm and redirecting it would make things worse, not better.
var buildCaches = []buildCache{
	// Rust. cargo locks its target dir, so parallel steps sharing one are
	// serialized by cargo rather than corrupting each other.
	{env: "CARGO_TARGET_DIR", dir: "cargo-target", marker: "Cargo.toml"},
}

// buildCacheEnv returns the cache-redirecting environment variables for the
// toolchains in use at worktreeDir, rooted at cacheRoot.
//
// It returns nothing when cacheRoot is empty (the caller could not resolve one),
// and skips any variable the caller's environment already sets — a developer or
// CI that has deliberately placed a cache somewhere must win over warden's
// default, or warden would silently relocate a cache someone else is managing.
func buildCacheEnv(cacheRoot, worktreeDir string, existing []string) []string {
	if cacheRoot == "" || worktreeDir == "" {
		return nil
	}
	var out []string
	for _, c := range buildCaches {
		if alreadySet(existing, c.env) {
			continue
		}
		if _, err := os.Stat(filepath.Join(worktreeDir, c.marker)); err != nil {
			continue
		}
		dir := filepath.Join(cacheRoot, c.dir)
		// Create it up front: a toolchain handed a path it cannot create will
		// usually fail the step rather than fall back, and a failure here should
		// cost the speed-up, never the run.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		out = append(out, c.env+"="+dir)
	}
	return out
}

// alreadySet reports whether env contains an assignment to key.
func alreadySet(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
