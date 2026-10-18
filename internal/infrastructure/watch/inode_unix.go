//go:build unix

package watch

import (
	"io/fs"
	"syscall"
)

// inode returns the file's inode number, so a file replaced by rename (how git
// writes the index) changes identity even if size and mtime collide.
func inode(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert // Ino is not uint64 on every unix
	}
	return 0
}
