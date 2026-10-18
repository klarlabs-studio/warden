//go:build !unix

package watch

import "io/fs"

// inode is unavailable here; size and mtime (plus the backstop) carry change
// detection alone.
func inode(fs.FileInfo) uint64 { return 0 }
