package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/infrastructure/buildcache"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
)

// repoBuildCache makes a repository, chdirs into it, and fills its build
// cache with size bytes. It returns the cache directory.
func repoBuildCache(t *testing.T, size int) string {
	t.Helper()
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := svc.BuildCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := os.MkdirAll(filepath.Join(dir, "cargo-target"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cargo-target", "lib.rlib"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubLegacy replaces the engine-facing container removal for one test.
func stubLegacy(t *testing.T, results []steps.LegacyContainerResult) {
	t.Helper()
	orig := removeLegacyContainers
	removeLegacyContainers = func(context.Context) []steps.LegacyContainerResult { return results }
	t.Cleanup(func() { removeLegacyContainers = orig })
}

func TestCmdClean_BuildCache(t *testing.T) {
	t.Run("removes the cache, then finds it empty", func(t *testing.T) {
		dir := repoBuildCache(t, 2048)
		code, out, errb := run("clean")
		if code != 0 || !strings.Contains(out, "removed build cache") || !strings.Contains(out, "2.0 KiB") {
			t.Fatalf("clean: code=%d out=%q err=%q", code, out, errb)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cache still present after clean: %v", err)
		}
		code, out, _ = run("clean")
		if code != 0 || !strings.Contains(out, "already empty") {
			t.Errorf("second clean: code=%d out=%q", code, out)
		}
	})
	t.Run("a busy cache is kept and exits with the contention code", func(t *testing.T) {
		dir := repoBuildCache(t, 2048)
		release, err := buildcache.Share(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		code, out, errb := run("clean")
		if code != exitContention || !strings.Contains(errb, "in use") || out != "" {
			t.Fatalf("clean: code=%d out=%q err=%q", code, out, errb)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("a busy cache was removed: %v", err)
		}
	})
	t.Run("invocation errors", func(t *testing.T) {
		repoBuildCache(t, 0)
		if code, _, errb := run("clean", "now"); code != 2 || !strings.Contains(errb, "positional") {
			t.Errorf("extra arg: code=%d err=%q", code, errb)
		}
		if code, _, _ := run("clean", "--bogus"); code != 2 {
			t.Errorf("unknown flag: code=%d", code)
		}
	})
	t.Run("outside a repository", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if code, _, errb := run("clean"); code != 1 || errb == "" {
			t.Errorf("outside a repo: code=%d err=%q", code, errb)
		}
	})
}

func TestCmdClean_LegacyContainers(t *testing.T) {
	tests := []struct {
		name     string
		results  []steps.LegacyContainerResult
		wantCode int
		wantOut  []string
		wantErr  []string
	}{
		{
			name:    "no engine",
			wantOut: []string{"no container engine installed"},
		},
		{
			name:    "nothing to remove",
			results: []steps.LegacyContainerResult{{Engine: "docker"}},
			wantOut: []string{"docker: no legacy warden containers"},
		},
		{
			name:    "removed",
			results: []steps.LegacyContainerResult{{Engine: "docker", Removed: []string{"warden-a", "warden-b"}}},
			wantOut: []string{"docker: removed 2 legacy container(s): warden-a, warden-b"},
		},
		{
			name:    "an engine that is down is skipped, not a failure",
			results: []steps.LegacyContainerResult{{Engine: "podman", Err: errors.New("no socket")}, {Engine: "apple", Removed: []string{"warden-c"}}},
			wantOut: []string{"apple: removed 1"},
			wantErr: []string{"podman: could not list containers (no socket); skipped"},
		},
		{
			name:     "a refused removal fails",
			results:  []steps.LegacyContainerResult{{Engine: "docker", Removed: []string{"warden-a"}, Failed: []string{"warden-b"}}},
			wantCode: 1,
			wantOut:  []string{"docker: removed 1"},
			wantErr:  []string{"docker: could not remove warden-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoBuildCache(t, 0)
			stubLegacy(t, tt.results)
			code, out, errb := run("clean", "--legacy-containers")
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d (out=%q err=%q)", code, tt.wantCode, out, errb)
			}
			if !strings.Contains(out, "build cache") {
				t.Errorf("the build cache is cleaned too: out=%q", out)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out, w) {
					t.Errorf("stdout %q lacks %q", out, w)
				}
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(errb, w) {
					t.Errorf("stderr %q lacks %q", errb, w)
				}
			}
		})
	}
	t.Run("a busy cache keeps its exit code", func(t *testing.T) {
		dir := repoBuildCache(t, 10)
		release, err := buildcache.Share(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		stubLegacy(t, []steps.LegacyContainerResult{{Engine: "docker", Failed: []string{"warden-b"}}})
		if code, _, _ := run("clean", "--legacy-containers"); code != exitContention {
			t.Errorf("code = %d, want %d", code, exitContention)
		}
	})
}

func TestBuildCacheLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "build-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 1536), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		max  string
		want []string
	}{
		{name: "default bound", max: "", want: []string{"1.5 KiB of a 5.0 GiB bound", buildcache.EnvMax, dir, "warden clean"}},
		{name: "override", max: "500MiB", want: []string{"of a 500.0 MiB bound"}},
		{name: "unbounded", max: "0", want: []string{"unbounded (" + buildcache.EnvMax + "=0)"}},
		{name: "unreadable bound", max: "huge", want: []string{"never trimmed", buildcache.EnvMax}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := buildCacheLine(dir, func(k string) string {
				if k == buildcache.EnvMax {
					return tt.max
				}
				return ""
			})
			for _, w := range tt.want {
				if !strings.Contains(line, w) {
					t.Errorf("line %q lacks %q", line, w)
				}
			}
		})
	}
	if line := buildCacheLine(filepath.Join(t.TempDir(), "missing"), func(string) string { return "" }); !strings.Contains(line, "0 B of") {
		t.Errorf("a missing cache should read as empty: %q", line)
	}
}

func TestCmdStatus_ShowsBuildCache(t *testing.T) {
	t.Setenv(buildcache.EnvMax, "")
	repoBuildCache(t, 100)
	code, out, errb := run("status")
	if code != 0 || !strings.Contains(out, "build cache: 100 B of a 5.0 GiB bound") {
		t.Errorf("status: code=%d out=%q err=%q", code, out, errb)
	}
}
