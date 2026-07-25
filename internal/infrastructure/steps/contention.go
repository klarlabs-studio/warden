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

// parallelRunnerHint offers the permanent cure when warden can see that the
// contending command is golangci-lint: its lock exists to stop concurrent runs
// from corrupting a SHARED cache, and warden already gives every run its own
// cache dir (see stepEnv), so opting out of the lock is safe here in a way it is
// not in general. Waiting out the lock fixes the symptom once; this fixes it for
// good, and only warden knows the precondition holds.
//
// The hint is withheld when the command already carries either opt-out flag, so
// a repo that has taken the advice is not told to take it again. Returns a
// trailing-newline-terminated line, or "" when there is nothing useful to say.
func parallelRunnerHint(command string) string {
	if !strings.Contains(command, "golangci-lint") {
		return ""
	}
	if strings.Contains(command, "--allow-parallel-runners") || strings.Contains(command, "--allow-serial-runners") {
		return ""
	}
	return "To stop this recurring, add --allow-parallel-runners to your lint command: " +
		"warden already isolates each run's GOLANGCI_LINT_CACHE, which is what the lock protects.\n"
}
