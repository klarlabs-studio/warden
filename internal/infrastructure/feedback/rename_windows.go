//go:build windows

package feedback

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Windows refuses to replace a file another process has open without
// FILE_SHARE_DELETE, which an editor reading status.json may well do. The
// rename is retried briefly; if the reader still holds it, this write is
// skipped and the next heartbeat writes again.
const (
	errorAccessDenied     syscall.Errno = 5
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

var renameRetryDelays = []time.Duration{
	10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
}

func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	for _, delay := range renameRetryDelays {
		if err == nil || !sharingViolation(err) {
			return err
		}
		time.Sleep(delay)
		err = os.Rename(src, dst)
	}
	return err
}

func sharingViolation(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errorSharingViolation || errno == errorLockViolation || errno == errorAccessDenied
}
