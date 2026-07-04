package hooks

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// TestShim_PreflightsBinary guards that the hook fails fast on an unrunnable
// binary (Gatekeeper-quarantined etc.) instead of hanging the commit/push.
func TestShim_PreflightsBinary(t *testing.T) {
	s := shim(domain.PreCommit, "1.2.3")
	for _, want := range []string{"_wd_timeout", "$bin\" --version", "not runnable", "--no-verify"} {
		if !strings.Contains(s, want) {
			t.Errorf("shim missing preflight element %q", want)
		}
	}
}
