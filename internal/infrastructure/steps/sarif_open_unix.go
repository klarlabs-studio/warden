//go:build unix

package steps

import "syscall"

// openNonBlocking is added to the report's open flags so that a FIFO swapped
// in after the regular-file check opens at once (to be refused by the Stat
// that follows) instead of blocking until a writer appears. It does not
// change how a regular file reads.
const openNonBlocking = syscall.O_NONBLOCK
