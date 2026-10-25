// Package buildcache bounds and removes warden's per-clone build cache: the
// directory steps redirect a compiled toolchain's cache into (see
// steps/buildcache.go), so a fresh worktree does not force a cold rebuild.
//
// Nothing else ever shrinks that directory. A Rust target dir grows with every
// toolchain, profile and dependency bump, and left alone it reaches tens of
// gigabytes in a clone nobody thinks of as large (#363). The bound is coarse
// on purpose: over it, the whole cache goes. A partial eviction would need to
// know which of a toolchain's files belong together, and losing the cache
// costs one cold build and nothing else.
//
// The cache is shared by every warden in the clone — a watcher and a hook can
// run at once — so removal is coordinated through a lock file beside the
// cache: runs hold it SHARED while their steps use the cache, and removal
// takes it EXCLUSIVE without waiting, skipping a busy cache rather than
// deleting under a running build.
package buildcache

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvMax names the environment variable that overrides the bound. An
// environment variable rather than a .warden.yaml key on purpose: an older
// warden rejects a configuration with a key it does not know, so a new key
// would block every commit on a machine that has not upgraded yet.
const EnvMax = "WARDEN_BUILD_CACHE_MAX"

// DefaultBound is the cache size above which a finished run removes it.
const DefaultBound int64 = 5 << 30

// ErrBusy means another warden run is using the cache, so it was not removed.
var ErrBusy = errors.New("the build cache is in use by a running warden")

// unitSizes maps a size suffix, lower-cased, to its multiplier. A bare K, M,
// G or T is binary, as in docker's and podman's --memory; the SI spellings
// are decimal.
var unitSizes = map[string]float64{
	"": 1, "b": 1,
	"k": 1 << 10, "kib": 1 << 10, "kb": 1e3,
	"m": 1 << 20, "mib": 1 << 20, "mb": 1e6,
	"g": 1 << 30, "gib": 1 << 30, "gb": 1e9,
	"t": 1 << 40, "tib": 1 << 40, "tb": 1e12,
}

// ParseSize reads a size such as "5GiB", "500MiB", "2G" or "1048576". Zero is
// valid and means unbounded.
func ParseSize(s string) (int64, error) {
	text := strings.TrimSpace(s)
	split := strings.IndexFunc(text, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	})
	number, unit := text, ""
	if split >= 0 {
		number, unit = text[:split], strings.TrimSpace(text[split:])
	}
	mult, ok := unitSizes[strings.ToLower(unit)]
	if number == "" || !ok {
		return 0, fmt.Errorf("invalid size %q: want a number with an optional unit, such as 5GiB or 500MiB", s)
	}
	n, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	bytes := n * mult
	if bytes >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return int64(bytes), nil
}

// BoundFrom resolves the bound from getenv: DefaultBound when EnvMax is unset
// or empty, 0 (unbounded) when it says so. A value that cannot be read is an
// error, and callers must then not evict — a misread bound must never be the
// reason a cache was deleted.
func BoundFrom(getenv func(string) string) (int64, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	v := strings.TrimSpace(getenv(EnvMax))
	if v == "" {
		return DefaultBound, nil
	}
	n, err := ParseSize(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", EnvMax, err)
	}
	return n, nil
}

// Size is the total size of the regular files under dir. Symlinks are counted
// as themselves and never followed. A missing dir is empty.
func Size(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// Eviction reports what a Trim or Clean found and did.
type Eviction struct {
	// Size is the cache's measured size before anything was removed.
	Size int64
	// Removed is true when the cache was deleted.
	Removed bool
	// Busy is true when another run held the cache, so it was left alone.
	Busy bool
}

// Trim removes the cache at dir when it is larger than bound, unless another
// run holds it. A bound of zero or less is unbounded and measures nothing.
//
// The size is measured before the lock is tried, so a run starting meanwhile
// is not kept waiting on the walk; the removal itself happens only under the
// exclusive lock, which is what makes it safe.
func Trim(dir string, bound int64) (Eviction, error) {
	if bound <= 0 {
		return Eviction{}, nil
	}
	size, err := Size(dir)
	if err != nil || size <= bound {
		return Eviction{Size: size}, err
	}
	ev, err := removeExclusive(dir)
	ev.Size = size
	return ev, err
}

// Clean removes the cache at dir whatever its size. It returns ErrBusy, and
// removes nothing, when another run holds the cache.
func Clean(dir string) (Eviction, error) {
	size, err := Size(dir)
	if err != nil {
		return Eviction{}, err
	}
	ev, err := removeExclusive(dir)
	ev.Size = size
	if err == nil && ev.Busy {
		err = ErrBusy
	}
	return ev, err
}

// removeExclusive deletes dir while holding the cache lock exclusively, and
// only then. A lock it cannot take — held by a run, or unavailable on this
// platform — removes nothing.
func removeExclusive(dir string) (Eviction, error) {
	f, err := openLock(dir)
	if err != nil {
		return Eviction{}, err
	}
	defer func() { _ = f.Close() }()
	acquired, err := tryLockExclusive(f)
	if err != nil {
		return Eviction{}, err
	}
	if !acquired {
		return Eviction{Busy: true}, nil
	}
	defer func() { _ = unlock(f) }()
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return Eviction{}, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return Eviction{}, err
	}
	return Eviction{Removed: true}, nil
}

// Share takes a shared hold on the cache at dir, waiting for a removal in
// progress to finish, and returns the function that ends it.
func Share(dir string) (func(), error) {
	f, err := openLock(dir)
	if err != nil {
		return nil, err
	}
	if err := lockShared(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unlock(f)
		_ = f.Close()
	}, nil
}

// LockPath is the lock file guarding the cache at dir. It sits BESIDE the
// cache rather than in it, so removing the cache never removes its lock.
func LockPath(dir string) string { return filepath.Clean(dir) + ".lock" }

func openLock(dir string) (*os.File, error) {
	path := LockPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
}

// Manager adapts the package to application.BuildCache, reading the bound
// from the environment on every trim.
type Manager struct {
	// Getenv reads EnvMax; nil means os.Getenv.
	Getenv func(string) string
}

// Share implements application.BuildCache.
func (Manager) Share(dir string) (func(), error) { return Share(dir) }

// Trim implements application.BuildCache. A bound that cannot be read
// evicts nothing.
func (m Manager) Trim(dir string) error {
	bound, err := BoundFrom(m.Getenv)
	if err != nil {
		return err
	}
	_, err = Trim(dir, bound)
	return err
}

// FormatSize renders n bytes in binary units, as the bound is usually written.
func FormatSize(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTP"[exp]) + "iB"
}
