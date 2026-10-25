//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package buildcache

import (
	"errors"
	"os"
)

// errUnsupported means this platform has no file lock warden can take, so
// removal is never safe and is skipped.
var errUnsupported = errors.New("build-cache locking is not supported on this platform")

// A platform without a file lock warden knows how to take can share the cache
// but never remove it: without the lock nothing can tell a busy cache from an
// idle one, and removing a cache under a running build is the one outcome this
// package exists to rule out. Such a cache stays unbounded.

func lockShared(*os.File) error { return nil }

func tryLockExclusive(*os.File) (bool, error) { return false, errUnsupported }

func unlock(*os.File) error { return nil }
