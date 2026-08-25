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
	// Comments sit ABOVE their element rather than trailing it. gofmt aligns a
	// column of trailing comments, and go1.26 and go1.27 align this particular
	// block differently — so a trailing-comment layout is clean under whichever
	// toolchain formatted it and flagged under the other, and warden's own lint
	// gate becomes unpassable for anyone whose Go differs from CI's. There is
	// nothing to align when each comment is on its own line.
	for _, want := range []string{
		// fetches the published digest list
		"checksums.txt",
		// reports a mismatch
		"CHECKSUM MISMATCH",
		// fails closed
		"refusing to execute an unverified binary",
		// user-only cache dir
		"chmod 700",
		// re-verify cached binary each run
		"failed its integrity check",
		// records the digest for re-verification
		"warden.sha256",
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
