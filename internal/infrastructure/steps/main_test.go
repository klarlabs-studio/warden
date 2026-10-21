package steps

import (
	"os"
	"testing"

	"go.klarlabs.de/warden/internal/testenv"
)

// TestMain points warden's config dir at a scratch directory for the whole
// package. Starting a container or a worktree records an owner, and the owner's
// machine id is created on first use under the config dir; a test run should
// not mint one in the developer's own. Tests that care set their own.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunWithScratchConfigDir(m))
}
