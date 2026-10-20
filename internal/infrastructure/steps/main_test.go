package steps

import (
	"os"
	"testing"
)

// TestMain points warden's config dir at a scratch directory for the whole
// package. Starting a container or a worktree records an owner, and the owner's
// machine id is created on first use under the config dir; a test run should
// not mint one in the developer's own. Tests that care set their own.
func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("WARDEN_CONFIG_DIR") != "" {
			return m.Run()
		}
		dir, err := os.MkdirTemp("", "warden-config-")
		if err != nil {
			panic(err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if err := os.Setenv("WARDEN_CONFIG_DIR", dir); err != nil {
			panic(err)
		}
		return m.Run()
	}()
	os.Exit(code)
}
