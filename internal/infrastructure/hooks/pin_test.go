package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestPinned(t *testing.T) {
	gitDir := t.TempDir()
	if err := Install(gitDir, domain.AllHooks, "0.17.0"); err != nil {
		t.Fatal(err)
	}
	pins := Pinned(gitDir)
	for _, h := range domain.AllHooks {
		if pins[h] != "0.17.0" {
			t.Errorf("Pinned[%s] = %q, want 0.17.0", h, pins[h])
		}
	}
}

func TestPinned_IgnoresForeignAndAbsentHooks(t *testing.T) {
	gitDir := t.TempDir()
	dir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hand-written hook is not ours to report a pin for, even if it happens
	// to carry a line that looks like one.
	foreign := "#!/bin/sh\n# pinned: 9.9.9\necho mine\n"
	if err := os.WriteFile(filepath.Join(dir, "pre-commit"), []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if pins := Pinned(gitDir); len(pins) != 0 {
		t.Errorf("Pinned should ignore unmanaged and absent hooks, got %v", pins)
	}
}

func TestPinnedVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"reads the pin line", "#!/bin/sh\n# pinned: 1.2.3\nver=\"1.2.3\"\n", "1.2.3"},
		{"trims trailing space", "# pinned: 1.2.3  \n", "1.2.3"},
		{"no pin line", "#!/bin/sh\necho hi\n", ""},
		{"empty pin value", "# pinned: \n", ""},
	}
	for _, tc := range tests {
		if got := pinnedVersion(tc.body); got != tc.want {
			t.Errorf("%s: pinnedVersion = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The shim must SAY when the binary it is about to exec is not the one the
// hook was pinned at. The pin is a bootstrap floor — PATH wins — so silence
// here is what makes version skew invisible at the moment it matters.
func TestShim_ReportsPinSkewForPathBinary(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	body := shim(domain.PreCommit, "0.17.0")

	// A fake `warden` on PATH that reports a different version. It must not
	// actually run the gate, so the shim's final exec is neutered by exiting 0
	// on the `run` subcommand.
	binDir := t.TempDir()
	fake := "#!/bin/sh\ncase \"$1\" in --version) echo 'warden 0.18.16' ;; *) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "warden"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	unboundPreflight(t, binDir)

	out, err := runShim(t, body, binDir)
	if err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}
	want := "warden: hook pins 0.17.0, PATH has 0.18.16 — running 0.18.16"
	if !strings.Contains(out, want) {
		t.Errorf("shim output missing skew line.\ngot:  %q\nwant: %q", out, want)
	}
}

// No skew, no noise: a matching PATH binary must not print anything.
func TestShim_SilentWhenPathMatchesPin(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	body := shim(domain.PreCommit, "0.18.16")

	binDir := t.TempDir()
	fake := "#!/bin/sh\ncase \"$1\" in --version) echo 'warden 0.18.16' ;; *) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "warden"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	unboundPreflight(t, binDir)

	out, err := runShim(t, body, binDir)
	if err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "hook pins") {
		t.Errorf("matching versions should print nothing, got %q", out)
	}
}

// unboundPreflight puts a pass-through `timeout` first on PATH, so the shim's
// 15s preflight budget does not become an assertion these tests never meant to
// make.
//
// That budget is a real product decision for a git hook and must not be tuned
// to a test's convenience. But a test that executes the shim end to end is
// asserting on wall clock it does not control: both skew tests failed at 15s+
// inside a full gate run — go test -race, a lint and a scanner competing for
// the same machine — while passing standalone in 0.3s (#228). The failure said
// nothing about the shim's behavior, which is what they are here to check.
//
// `_wd_timeout` prefers `timeout` and returns as soon as it finds one, so a
// shim of that name in binDir wins. It drops the duration and execs, which is
// the same code path the shim takes on a machine with no timeout tool at all.
func unboundPreflight(t *testing.T, binDir string) {
	t.Helper()
	const passthrough = "#!/bin/sh\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "timeout"), []byte(passthrough), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A preflight that timed out has not learned that the binary is broken — only
// that it did not answer. Saying "Gatekeeper-quarantined, corrupt, or blocked"
// there sends a developer whose machine was merely busy to fix a binary that
// was never wrong, which is the wrong-diagnosis defect this project treats as
// its central failure mode.
func TestShim_TimeoutIsNotReportedAsABrokenBinary(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	binDir := t.TempDir()
	fake := "#!/bin/sh\ncase \"$1\" in --version) echo 'warden 0.18.16' ;; *) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "warden"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// A `timeout` that always reports the timeout convention, so the preflight
	// takes its 124 branch without the test waiting 15 real seconds.
	if err := os.WriteFile(filepath.Join(binDir, "timeout"),
		[]byte("#!/bin/sh\nexit 124\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runShim(t, shim(domain.PreCommit, "0.18.16"), binDir)
	if err == nil {
		t.Fatal("a preflight that could not check the binary must still fail closed")
	}
	if !strings.Contains(out, "did not answer --version within 15s") {
		t.Errorf("timeout must name the timeout.\ngot: %q", out)
	}
	if strings.Contains(out, "quarantined") || strings.Contains(out, "corrupt") {
		t.Errorf("a timeout must not be diagnosed as a broken binary.\ngot: %q", out)
	}
}

// runShim executes a shim body with binDir prepended to PATH, returning its
// combined output.
func runShim(t *testing.T, body, binDir string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}
