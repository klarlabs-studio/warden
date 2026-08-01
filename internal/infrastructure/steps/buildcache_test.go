package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCacheEnv_RedirectsWhenTheToolchainIsPresent(t *testing.T) {
	root := t.TempDir()
	wt := t.TempDir()
	touch(t, filepath.Join(wt, "Cargo.toml"))

	got := buildCacheEnv(root, wt, nil)
	if len(got) != 1 {
		t.Fatalf("env = %v, want one CARGO_TARGET_DIR assignment", got)
	}
	if !strings.HasPrefix(got[0], "CARGO_TARGET_DIR=") {
		t.Errorf("env = %q, want CARGO_TARGET_DIR", got[0])
	}
	// The directory must exist: a toolchain handed a path it cannot create
	// usually fails the step rather than falling back.
	dir := strings.TrimPrefix(got[0], "CARGO_TARGET_DIR=")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("cache dir %q was not created: %v", dir, err)
	}
}

// Detection is by marker on purpose. Setting a cache variable for a toolchain
// the repo does not use would create empty directories and make warden look
// like the cause if that toolchain later misbehaved.
func TestBuildCacheEnv_SilentWithoutTheMarker(t *testing.T) {
	if got := buildCacheEnv(t.TempDir(), t.TempDir(), nil); got != nil {
		t.Errorf("a repo with no Cargo.toml should get no redirection, got %v", got)
	}
}

// A developer or CI that deliberately placed a cache somewhere must win —
// otherwise warden silently relocates a cache someone else is managing.
func TestBuildCacheEnv_DoesNotOverrideAnExplicitSetting(t *testing.T) {
	wt := t.TempDir()
	touch(t, filepath.Join(wt, "Cargo.toml"))

	existing := []string{"CARGO_TARGET_DIR=/somewhere/deliberate"}
	if got := buildCacheEnv(t.TempDir(), wt, existing); got != nil {
		t.Errorf("an explicitly set variable must not be overridden, got %v", got)
	}
}

// Cache reuse is an optimization; a run must behave identically without it. An
// unresolvable git dir therefore yields no redirection rather than an error.
func TestBuildCacheEnv_EmptyInputsDisableItQuietly(t *testing.T) {
	wt := t.TempDir()
	touch(t, filepath.Join(wt, "Cargo.toml"))

	if got := buildCacheEnv("", wt, nil); got != nil {
		t.Errorf("no cache root should disable redirection, got %v", got)
	}
	if got := buildCacheEnv(t.TempDir(), "", nil); got != nil {
		t.Errorf("no worktree should disable redirection, got %v", got)
	}
}

// Two toolchains in one repo must never share a directory, or one compiler's
// output would be handed to another.
func TestBuildCaches_HaveDistinctDirsAndVars(t *testing.T) {
	dirs := map[string]bool{}
	vars := map[string]bool{}
	for _, c := range buildCaches {
		if c.env == "" || c.dir == "" || c.marker == "" {
			t.Errorf("incomplete build cache entry: %+v", c)
		}
		if dirs[c.dir] {
			t.Errorf("duplicate cache dir %q", c.dir)
		}
		if vars[c.env] {
			t.Errorf("duplicate cache var %q", c.env)
		}
		dirs[c.dir], vars[c.env] = true, true
	}
}

// Go's build cache already lives outside the repo, so a fresh worktree is
// already warm. Redirecting it would make things worse, not better — this pins
// the reasoning so a later "add every toolchain" sweep has to argue with it.
func TestBuildCaches_ExcludeGo(t *testing.T) {
	for _, c := range buildCaches {
		if c.env == "GOCACHE" || c.marker == "go.mod" {
			t.Errorf("Go's cache is already outside the repo and must not be redirected: %+v", c)
		}
	}
}

func TestAlreadySet(t *testing.T) {
	env := []string{"FOO=1", "CARGO_TARGET_DIR=/x", "BAR=2"}
	if !alreadySet(env, "CARGO_TARGET_DIR") {
		t.Error("should find an existing assignment")
	}
	if alreadySet(env, "CARGO") {
		t.Error("a prefix of a real key must not match")
	}
	if alreadySet(env, "BAZ") {
		t.Error("should not find a missing key")
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
