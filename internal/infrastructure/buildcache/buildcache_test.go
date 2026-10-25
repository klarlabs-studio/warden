package buildcache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fill creates dir with one file of size bytes, standing in for a toolchain's
// cache, and returns dir.
func fill(t *testing.T, dir string, size int) string {
	t.Helper()
	sub := filepath.Join(dir, "cargo-target", "debug")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "artifact"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "1048576", want: 1 << 20},
		{in: "5GiB", want: 5 << 30},
		{in: "500MiB", want: 500 << 20},
		{in: "500mib", want: 500 << 20},
		{in: "2G", want: 2 << 30},
		{in: "2g", want: 2 << 30},
		{in: "1.5GiB", want: 3 << 29},
		{in: "  64 KiB ", want: 64 << 10},
		{in: "1GB", want: 1e9},
		{in: "1TiB", want: 1 << 40},
		{in: "10B", want: 10},
		{in: "", wantErr: true},
		{in: "GiB", wantErr: true},
		{in: "-1GiB", wantErr: true},
		{in: "5 parsecs", wantErr: true},
		{in: "1.2.3G", wantErr: true},
		{in: "99999999999TiB", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSize(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSize(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestBoundFrom(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    int64
		wantErr bool
	}{
		{name: "unset is the default", env: nil, want: DefaultBound},
		{name: "blank is the default", env: map[string]string{EnvMax: "  "}, want: DefaultBound},
		{name: "zero is unbounded", env: map[string]string{EnvMax: "0"}, want: 0},
		{name: "override", env: map[string]string{EnvMax: "500MiB"}, want: 500 << 20},
		{name: "unreadable is an error, never a bound", env: map[string]string{EnvMax: "lots"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BoundFrom(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("bound = %d, want %d", got, tt.want)
			}
		})
	}
	if DefaultBound != 5<<30 {
		t.Errorf("DefaultBound = %d, want 5 GiB", DefaultBound)
	}
}

// TestTrim is the bound itself: over it the whole cache goes, at or under it
// nothing does, and a cache another run holds is never removed whatever its
// size.
func TestTrim(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		bound       int64
		busy        bool
		wantRemoved bool
		wantBusy    bool
	}{
		{name: "over the bound evicts", size: 2048, bound: 1024, wantRemoved: true},
		{name: "under the bound keeps", size: 512, bound: 1024},
		{name: "exactly the bound keeps", size: 1024, bound: 1024},
		{name: "unbounded keeps", size: 1 << 20, bound: 0},
		{name: "busy over the bound keeps", size: 2048, bound: 1024, busy: true, wantBusy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fill(t, filepath.Join(t.TempDir(), "build-cache"), tt.size)
			if tt.busy {
				release, err := Share(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			ev, err := Trim(dir, tt.bound)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Removed != tt.wantRemoved || ev.Busy != tt.wantBusy {
				t.Errorf("eviction = %+v, want removed=%v busy=%v", ev, tt.wantRemoved, tt.wantBusy)
			}
			if exists(t, dir) == tt.wantRemoved {
				t.Errorf("cache present = %v after an eviction that reported removed = %v", !tt.wantRemoved, tt.wantRemoved)
			}
			if !exists(t, LockPath(dir)) && tt.bound > 0 && tt.size > int(tt.bound) {
				t.Error("the lock must sit beside the cache and survive its removal")
			}
		})
	}
}

// TestTrim_AfterReleaseEvicts pins that the shared hold, once released, no
// longer protects the cache: a hold that leaked would leave it unbounded.
func TestTrim_AfterReleaseEvicts(t *testing.T) {
	dir := fill(t, filepath.Join(t.TempDir(), "build-cache"), 2048)
	release, err := Share(dir)
	if err != nil {
		t.Fatal(err)
	}
	release()
	ev, err := Trim(dir, 1024)
	if err != nil || !ev.Removed {
		t.Fatalf("Trim after release = %+v, %v; want removed", ev, err)
	}
}

func TestClean(t *testing.T) {
	t.Run("removes the cache whatever its size", func(t *testing.T) {
		dir := fill(t, filepath.Join(t.TempDir(), "build-cache"), 10)
		ev, err := Clean(dir)
		if err != nil || !ev.Removed || ev.Size != 10 {
			t.Fatalf("Clean = %+v, %v; want removed, size 10", ev, err)
		}
		if exists(t, dir) {
			t.Error("cache still present")
		}
	})
	t.Run("a missing cache is not reported as removed", func(t *testing.T) {
		ev, err := Clean(filepath.Join(t.TempDir(), "build-cache"))
		if err != nil || ev.Removed {
			t.Fatalf("Clean = %+v, %v; want nothing removed", ev, err)
		}
	})
	t.Run("a busy cache is refused and kept", func(t *testing.T) {
		dir := fill(t, filepath.Join(t.TempDir(), "build-cache"), 10)
		release, err := Share(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ev, err := Clean(dir)
		if !errors.Is(err, ErrBusy) || ev.Removed {
			t.Fatalf("Clean = %+v, %v; want ErrBusy", ev, err)
		}
		if !exists(t, dir) {
			t.Error("a busy cache was removed")
		}
	})
}

func TestSize(t *testing.T) {
	root := t.TempDir()
	if got, err := Size(filepath.Join(root, "missing")); err != nil || got != 0 {
		t.Fatalf("Size(missing) = %d, %v; want 0", got, err)
	}
	dir := fill(t, filepath.Join(root, "build-cache"), 100)
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Size(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The symlink counts as itself (a few bytes), never as its 1 MiB target.
	if got < 100 || got >= 1<<20 {
		t.Errorf("Size = %d, want the cache's own bytes without following the symlink", got)
	}
}

func TestManagerTrim(t *testing.T) {
	tests := []struct {
		name        string
		max         string
		wantRemoved bool
		wantErr     bool
	}{
		{name: "env bound applies", max: "1KiB", wantRemoved: true},
		{name: "default bound keeps a small cache", max: ""},
		{name: "zero disables eviction", max: "0"},
		{name: "unreadable bound evicts nothing", max: "tiny", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fill(t, filepath.Join(t.TempDir(), "build-cache"), 4096)
			m := Manager{Getenv: func(k string) string {
				if k == EnvMax {
					return tt.max
				}
				return ""
			}}
			err := m.Trim(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Trim error = %v, wantErr %v", err, tt.wantErr)
			}
			if exists(t, dir) == tt.wantRemoved {
				t.Errorf("removed = %v, want %v", !exists(t, dir), tt.wantRemoved)
			}
		})
	}
	release, err := Manager{}.Share(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 << 30, "5.0 GiB"},
		{3 << 40, "3.0 TiB"},
	}
	for _, tt := range tests {
		if got := FormatSize(tt.in); got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
