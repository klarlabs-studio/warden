package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The npm wrapper is the entry point for every `npx @klarlabs-studio/warden`
// user and had no tests. What it does when the platform binary is missing
// matters more than the happy path, because that is the branch a user meets on
// their worst day — and until now it gave one message for two failures with
// opposite fixes:
//
//   - the platform genuinely has no published binary → install another way
//   - the binary IS published but is not installed here → reinstall
//
// Telling someone their platform is unsupported when a reinstall would fix it
// sends them to build from source to solve a transient problem. That is not a
// crash, it is a wrong diagnosis, which is the defect class this project keeps
// finding: a message that names a cause the evidence does not support.
//
// The second case is not hypothetical. npm's registry propagates a publish to
// the packument on its own schedule, and warden's last two releases both had
// exactly one platform package (`win32-x64`, published last) resolve minutes
// after the wrapper did. Anyone installing in that window gets the wrapper with
// its optional dependency silently skipped.
const wrapperSrc = "../npm/bin/warden.cjs"

// wrapperHarness stages a copy of the wrapper beside a package.json, which is
// what the wrapper reads to decide whether a platform is published at all.
func wrapperHarness(t *testing.T, pkgJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src, err := os.ReadFile(wrapperSrc)
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "warden.cjs"), src, 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	return dir
}

func runWrapper(t *testing.T, dir string) (string, int) {
	t.Helper()
	node := requireNode(t)
	cmd := exec.Command(node, filepath.Join(dir, "bin", "warden.cjs"), "version")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run wrapper: %v", err)
	}
	return string(out), code
}

// A platform warden publishes for, whose package is simply not installed, must
// be diagnosed as an install problem — the one a reinstall fixes.
func TestNpmWrapper_PublishedButNotInstalledSaysReinstall(t *testing.T) {
	// The wrapper derives the package name from the running platform, so the
	// harness must claim to publish for whatever this test runs on.
	dir := wrapperHarness(t, `{"optionalDependencies":{"@klarlabs-studio/warden-`+nodePlatformArch(t)+`":"9.9.9"}}`)
	out, code := runWrapper(t, dir)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.Contains(out, "no prebuilt binary") {
		t.Errorf("reports the platform as unsupported when warden publishes for it:\n%s", out)
	}
	for _, want := range []string{"not installed here", "install problem", "npm install"} {
		if !strings.Contains(out, want) {
			t.Errorf("message lacks %q, so it does not point at the fix:\n%s", want, out)
		}
	}
}

// A platform warden does NOT publish for must keep the original message. The
// fix there really is to install another way, and telling that user to reinstall
// would loop them forever.
func TestNpmWrapper_UnpublishedPlatformSaysUnsupported(t *testing.T) {
	dir := wrapperHarness(t, `{"optionalDependencies":{"@klarlabs-studio/warden-aix-ppc64":"9.9.9"}}`)
	out, code := runWrapper(t, dir)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "no prebuilt binary") {
		t.Errorf("lost the unsupported-platform message:\n%s", out)
	}
	if strings.Contains(out, "npm install @klarlabs-studio/warden-") {
		t.Errorf("tells the user to install a package warden does not publish:\n%s", out)
	}
}

// When the wrapper cannot determine whether a platform is published, it must
// make the WEAKER claim. Asserting "this is published, reinstall it" without
// having checked would be the same over-claim in the other direction.
//
// Both ways of not knowing are covered, because they take different code paths
// and only one of them was tested at first: an EMPTY manifest answers the
// question (no such key) while a MALFORMED or ABSENT one throws. A version of
// this test using only `{}` passed happily against a wrapper whose catch block
// returned "published" — the mutation survived, and the test's own name was
// the tell that it had not exercised what it claimed.
func TestNpmWrapper_CannotTellFallsBackToTheWeakerClaim(t *testing.T) {
	for name, manifest := range map[string]string{
		"empty object":    `{}`,
		"malformed json":  `{"optionalDependencies":`,
		"not json at all": `this is not json`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := wrapperHarness(t, manifest)
			out, code := runWrapper(t, dir)

			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(out, "no prebuilt binary") {
				t.Errorf("cannot tell whether the platform is published, so want the weaker message:\n%s", out)
			}
			if strings.Contains(out, "is published for this release") {
				t.Errorf("claims the platform is published without having established it:\n%s", out)
			}
		})
	}
}

// The manifest being absent entirely is the same "cannot tell" case, but it
// cannot go through wrapperHarness, which always writes one.
func TestNpmWrapper_MissingManifestFallsBackToTheWeakerClaim(t *testing.T) {
	dir := wrapperHarness(t, `{}`)
	if err := os.Remove(filepath.Join(dir, "package.json")); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	out, code := runWrapper(t, dir)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.Contains(out, "is published for this release") {
		t.Errorf("with no manifest at all, claims the platform is published:\n%s", out)
	}
}

// nodePlatformArch asks node what it calls this platform, rather than mapping
// GOOS/GOARCH by hand — the wrapper builds the package name from node's own
// values, so a hand-written table would test a different string than ships.
func nodePlatformArch(t *testing.T) string {
	t.Helper()
	node := requireNode(t)
	out, err := exec.Command(node, "-p", "process.platform+'-'+process.arch").Output()
	if err != nil {
		t.Fatalf("ask node for platform: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// requireNode locates node, and refuses to let its absence pass quietly where
// node is supposed to exist.
//
// Skipping on a missing dependency is the right call on a contributor's laptop
// and the wrong one in CI, where the whole point is that these ran. Without
// `-v`, `go test` prints nothing at all for a skipped test — a skip and a pass
// are indistinguishable in the log — so a silent skip would report this file as
// covered while it covered nothing. GitHub-hosted runners ship node, so in CI
// its absence is a broken environment and must be loud.
func requireNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err == nil {
		return node
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("node not on PATH in CI, so the npm wrapper went untested: %v", err)
	}
	t.Skipf("node not on PATH, wrapper left untested locally: %v", err)
	return ""
}
