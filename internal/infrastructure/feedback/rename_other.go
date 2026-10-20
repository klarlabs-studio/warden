//go:build !windows

package feedback

import "os"

// replaceFile renames src over dst. POSIX rename replaces atomically even
// while a reader has dst open.
func replaceFile(src, dst string) error { return os.Rename(src, dst) }
