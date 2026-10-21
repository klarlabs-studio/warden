package service

import (
	"os"
	"testing"

	"go.klarlabs.de/warden/internal/testenv"
)

// TestMain points warden's config dir at a scratch directory for the whole
// package. The service loads the signing key, the trust store and the machine
// id from the config dir; read from the developer's own, a test's outcome
// would depend on that machine, and the run would mint a key there. Tests that
// care set their own.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunWithScratchConfigDir(m))
}
