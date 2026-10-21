package kernel

import (
	"os"
	"testing"

	"go.klarlabs.de/warden/internal/testenv"
)

// TestMain points warden's config dir at a scratch directory for the whole
// package: running a step records its owner, whose machine id is created on
// first use under the config dir, and a test run must not mint one in the
// developer's own. Tests that care set their own.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunWithScratchConfigDir(m))
}
