//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package buildcache

import (
	"errors"
	"os"
	"syscall"
)

// flock(2) locks belong to the open file description, so two opens of the
// lock file conflict even within one process, and the kernel releases a lock
// when its holder dies — a crashed run never leaves the cache locked.

func lockShared(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func tryLockExclusive(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue
		default:
			return false, err
		}
	}
}

func unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
