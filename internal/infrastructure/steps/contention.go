package steps

import "strings"

// Tools that guard a shared resource with a mutex refuse to start while another
// instance holds it. That refusal is NOT a verdict on the tree: the check never
// ran. Reporting it as a step failure sends the developer looking for a lint
// error that does not exist — and it is usually self-inflicted (run the linter
// in one terminal, commit from another, get "failed" back).
//
// The signatures are matched case-insensitively against the command's combined
// output. They are deliberately narrow: a false positive would retry a genuine
// failure and, worse, could soften it into "could not run". Each entry names a
// message a tool emits *instead of* doing any work.
var contentionSignatures = []string{
	// golangci-lint, when another invocation holds its lock.
	"parallel golangci-lint is running",
	// cargo / rustup: "Blocking waiting for file lock on build directory".
	"blocking waiting for file lock",
	// Common single-instance guards.
	"another instance is already running",
	"could not acquire lock",
	"unable to acquire lock",
	"resource temporarily unavailable (os error 11)",
}

// isContention reports whether output shows the command declined to run because
// another process holds its lock, rather than having run and found a problem.
func isContention(output string) bool {
	lower := strings.ToLower(output)
	for _, sig := range contentionSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}
