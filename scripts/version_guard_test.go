// Package scripts_test exercises the shipped installer scripts. They are not Go
// code, but they are release artifacts users pipe into a shell, so they need the
// same regression pressure as the binary.
//
// These tests drive the REAL scripts rather than a re-implementation of their
// guard. A copy of the regex asserted against itself would pass forever while
// the shipped script drifted, which is the failure mode that let this class in.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostileVersions are values for WARDEN_VERSION that must never reach a URL.
//
// The traversal cases are the reason this guard exists. "$base" is built as
// https://github.com/klarlabs-studio/warden/releases/download/$VERSION, so four
// levels of ".." walk the path back to the host root and the archive *and*
// checksums.txt both resolve to an attacker's repository. The checksum step
// cannot catch that: it verifies the download against a checksums.txt fetched
// from the same attacker-controlled base, so it confirms the attacker's file
// matches the attacker's digest.
var hostileVersions = []struct{ name, version string }{
	{"path traversal to another repo", "../../../../attacker/evil/releases/download/v1"},
	{"single level traversal", "../evil"},
	{"absolute path", "/attacker/evil"},
	{"protocol-relative host", "//attacker.example.com/x"},
	{"embedded scheme", "https://attacker.example.com/x"},
	{"query string appended", "v0.20.4?x=/../../attacker/evil"},
	{"fragment appended", "v0.20.4#/../../attacker/evil"},
	{"trailing slash path", "v0.20.4/../../attacker/evil"},
	{"command substitution", "v0.20.4$(id)"},
	{"backtick substitution", "v0.20.4`id`"},
	{"semicolon", "v0.20.4;id"},
	// grep matches line by line and .NET's `$` tolerates one trailing newline,
	// so each of these smuggles a value past a guard that looks anchored.
	{"newline injection", "v0.20.4\nid"},
	{"trailing newline", "v0.20.4\n"},
	{"leading newline", "\nv0.20.4"},
	{"carriage return", "v0.20.4\r"},
	{"space separated", "v0.20.4 --foo"},
	{"empty-ish", "."},
	{"not a version", "latest-ish"},
}

// wellFormed are legitimate release-tag shapes the guard must let through, so
// the fix does not quietly break installing a real version.
//
// Deliberately versions that do not exist: the assertion is only that the guard
// let them reach the download, and a tag that resolves would pull a real
// multi-megabyte release and install it on every run. A 404 proves the same
// thing in a fraction of the time. It also means a network outage cannot turn
// these red — the run fails at the download either way, which is not the
// failure these tests look for.
var wellFormed = []string{"v99.99.99", "99.99.99", "v99.99.99-rc1", "v99.99.0-rc.1", "99.99.99+build.5"}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", rel))
	if err != nil {
		t.Fatalf("resolve %s: %v", rel, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("installer missing at %s: %v", abs, err)
	}
	return abs
}

// runInstaller executes an installer with WARDEN_VERSION set, in a throwaway
// HOME so a passing run cannot touch the developer's real ~/.warden.
func runInstaller(t *testing.T, argv []string, version string) (string, error) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // fixed argv from the test table
	cmd.Env = append(os.Environ(),
		"WARDEN_VERSION="+version,
		"HOME="+home,
		"WARDEN_BIN_DIR="+filepath.Join(home, "bin"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// assertRefused checks the installer rejected version with our guard's message.
// Matching the message matters: any bad URL eventually fails somewhere, so a
// bare non-zero exit would also pass while the request had already gone out.
func assertRefused(t *testing.T, argv []string, version string) {
	t.Helper()
	out, err := runInstaller(t, argv, version)
	if err == nil {
		t.Fatalf("installer accepted hostile version %q (output: %s)", version, out)
	}
	if !strings.Contains(out, "refusing version") {
		t.Errorf("version %q was rejected, but not by the guard — got: %s", version, strings.TrimSpace(out))
	}
	// The guard runs before any download, so nothing should have been fetched.
	if strings.Contains(out, "downloading warden") {
		t.Errorf("version %q reached the download step: %s", version, strings.TrimSpace(out))
	}
}

// assertPastGuard checks a legitimate tag is NOT stopped by the guard. It will
// still fail afterwards (the release does not exist / no network), which is
// fine — the assertion is only that the refusal did not come from the guard.
func assertPastGuard(t *testing.T, argv []string, version string) {
	t.Helper()
	out, _ := runInstaller(t, argv, version)
	if strings.Contains(out, "refusing version") {
		t.Errorf("guard rejected the well-formed tag %q: %s", version, strings.TrimSpace(out))
	}
}

func TestInstallShRefusesHostileVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	sh := repoFile(t, "scripts/install.sh")
	for _, c := range hostileVersions {
		t.Run(c.name, func(t *testing.T) {
			assertRefused(t, []string{"sh", sh}, c.version)
		})
	}
}

func TestInstallShAcceptsReleaseTags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	sh := repoFile(t, "scripts/install.sh")
	for _, v := range wellFormed {
		t.Run(v, func(t *testing.T) { assertPastGuard(t, []string{"sh", sh}, v) })
	}
}

func TestInstallActionShRefusesHostileVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	sh := repoFile(t, ".github/actions/install-warden.sh")
	for _, c := range hostileVersions {
		t.Run(c.name, func(t *testing.T) {
			assertRefused(t, []string{"bash", sh}, c.version)
		})
	}
}

func TestInstallActionShAcceptsReleaseTags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	sh := repoFile(t, ".github/actions/install-warden.sh")
	for _, v := range wellFormed {
		t.Run(v, func(t *testing.T) { assertPastGuard(t, []string{"bash", sh}, v) })
	}
}

// pwsh is preinstalled on every GitHub-hosted runner, so these run in CI even
// though CI is ubuntu-only. Locally they skip unless PowerShell is installed —
// which is exactly why install.ps1 could carry this flaw unnoticed: nothing in
// the repo could execute it.
func powershell(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not installed; install PowerShell to exercise install.ps1")
	}
	return p
}

func TestInstallPs1RefusesHostileVersion(t *testing.T) {
	pwsh := powershell(t)
	ps1 := repoFile(t, "scripts/install.ps1")
	for _, c := range hostileVersions {
		t.Run(c.name, func(t *testing.T) {
			assertRefused(t, []string{pwsh, "-NoProfile", "-File", ps1}, c.version)
		})
	}
}

func TestInstallPs1AcceptsReleaseTags(t *testing.T) {
	pwsh := powershell(t)
	ps1 := repoFile(t, "scripts/install.ps1")
	for _, v := range wellFormed {
		t.Run(v, func(t *testing.T) {
			assertPastGuard(t, []string{pwsh, "-NoProfile", "-File", ps1}, v)
		})
	}
}
