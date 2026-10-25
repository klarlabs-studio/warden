//go:build windows

package buildcache

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx locks belong to the handle, so two opens of the lock file
// conflict even within one process, and Windows releases a lock when its
// holder's handle closes, including when the process dies.

// lockRange is the byte range locked: the first byte, which is all a lock
// file needs whatever its length.
const lockRange = 1

func lockShared(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), 0, 0, lockRange, 0, new(windows.Overlapped))
}

func tryLockExclusive(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockRange, 0, new(windows.Overlapped))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRange, 0, new(windows.Overlapped))
}
