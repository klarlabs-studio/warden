//go:build !unix

package steps

// openNonBlocking is zero where there are no FIFOs to open in the worktree:
// opening a file there never waits for a writer.
const openNonBlocking = 0
