// Package testenv holds process-wide test setup shared by package TestMains.
package testenv

import (
	"os"
	"testing"
)

// RunWithScratchConfigDir runs m with warden's config dir (WARDEN_CONFIG_DIR)
// pointed at a fresh scratch directory, removed afterwards, and returns the
// exit code for os.Exit. A test run must not read the developer's own config
// dir, or write to it: the signing key and the machine id are created there on
// first use, and a test that reads a real key, roster or trust store passes or
// fails by whose machine it runs on. A WARDEN_CONFIG_DIR already set is kept,
// and tests that care about the directory set their own with t.Setenv.
func RunWithScratchConfigDir(m *testing.M) int {
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
}
