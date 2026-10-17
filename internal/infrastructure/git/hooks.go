package git

import (
	"fmt"
	"path/filepath"
)

// HookPaths asks Git for both its default shared hook directory and the path
// it will actually consult. --git-path honors core.hooksPath, including config
// inherited from global, conditional, command, and worktree scopes.
func (r *Repo) HookPaths() (expected, effective string, err error) {
	common, err := r.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	effective, err = r.run("rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", "", err
	}
	return filepath.Join(common, "hooks"), filepath.Clean(effective), nil
}

// SameHookPath accepts aliases of an existing directory, but never treats a
// failed resolution as proof that two different paths name the same place.
func SameHookPath(expected, effective string) bool {
	if filepath.Clean(expected) == filepath.Clean(effective) {
		return true
	}
	a, errA := filepath.EvalSymlinks(expected)
	b, errB := filepath.EvalSymlinks(effective)
	return errA == nil && errB == nil && a == b
}

func HookPathConflict(expected, effective string) string {
	return fmt.Sprintf("Git uses hooks at %q, but Warden installs at %q; review core.hooksPath and existing hooks before selecting Warden's directory (see docs/hook-ecosystem-bridge.md)", effective, expected)
}
