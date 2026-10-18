//go:build !unix

package steps

import "testing"

// Ownership is a Unix property; the container integration test does not run
// on other hosts.
func assertOwnedByInvoker(t *testing.T, _ string) { t.Helper() }
