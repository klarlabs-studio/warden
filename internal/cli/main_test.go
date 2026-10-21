package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// idleLoad is the machine reading every test in this package sees unless it
// forces another one.
//
// A failing step carries a load caveat in its Why whenever the box is
// oversubscribed, and the renderers this package tests show it — an extra
// "why" column in axi, an extra line in run output. Read from the real machine,
// that made assertions about the ordinary output shape fail whenever the suite
// shared a busy box (CI, several agents), which is a property of the machine
// and not of the change under test. Tests that are about the caveat force
// contention with proc.SetLoadProbe and restore this reading afterwards.
var idleLoad = proc.Load{Known: true, Value: 0.5, CPUs: 8}

// envVanishStartDir asks a re-executed test binary to delete the directory it
// was started in before any test runs, so a test can prove the suite does not
// depend on that directory still existing. See
// TestCmdAxi_IndependentOfTheStartingDirectory.
const envVanishStartDir = "WARDEN_TEST_VANISH_START_DIR"

// vanishStartPrefix guards the removal: only an empty directory with this name
// prefix is ever removed, so a stray variable cannot delete a real checkout.
const vanishStartPrefix = "warden-vanish-"

func TestMain(m *testing.M) {
	proc.SetLoadProbe(func() proc.Load { return idleLoad })
	if os.Getenv(envVanishStartDir) == "1" {
		if wd, err := os.Getwd(); err == nil && strings.HasPrefix(filepath.Base(wd), vanishStartPrefix) {
			_ = os.Remove(wd)
		}
	}
	os.Exit(m.Run())
}

// forceLoad makes every step in the calling test run on a machine reading l.
// Not for parallel tests: the probe is process-wide.
func forceLoad(t *testing.T, l proc.Load) {
	t.Helper()
	t.Cleanup(proc.SetLoadProbe(func() proc.Load { return l }))
}
