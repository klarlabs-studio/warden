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

// TestShim_VerifiesChecksumBeforeExec guards the supply-chain fix: the
// self-fetched tarball must be SHA-256-verified against the release's
// checksums.txt, the check must fail closed, and it must happen *before* the
// binary is made executable. It also asserts the cheap cache re-verification
// and the 0700 cache directory.
func TestShim_VerifiesChecksumBeforeExec(t *testing.T) {
	s := shim(domain.PreCommit, "1.2.3")

	// Structural elements that must be present.
	for _, want := range []string{
		"checksums.txt",                            // fetches the published digest list
		"CHECKSUM MISMATCH",                        // reports a mismatch
		"refusing to execute an unverified binary", // fails closed
		"chmod 700",                                // user-only cache dir
		"failed its integrity check",               // re-verify cached binary each run
		"warden.sha256",                            // records the digest for re-verification
	} {
		if !strings.Contains(s, want) {
			t.Errorf("shim missing supply-chain element %q\n---\n%s", want, s)
		}
	}

	// Fail-closed: the mismatch branch must exit non-zero.
	mismatch := strings.Index(s, "CHECKSUM MISMATCH")
	if mismatch < 0 {
		t.Fatal("no checksum-mismatch branch")
	}
	if !strings.Contains(s[mismatch:], "exit 1") {
		t.Error("checksum mismatch must exit 1 (fail closed)")
	}

	// Ordering: verification must precede making the binary executable/exec.
	verify := strings.Index(s, `"$want" != "$got"`)
	chmodX := strings.Index(s, "chmod +x")
	execAt := strings.Index(s, `exec "$bin"`)
	if verify < 0 || chmodX < 0 || execAt < 0 {
		t.Fatalf("missing markers: verify=%d chmodX=%d exec=%d", verify, chmodX, execAt)
	}
	if verify >= chmodX || verify >= execAt {
		t.Errorf("checksum verification (%d) must come before chmod +x (%d) and exec (%d)", verify, chmodX, execAt)
	}
}
