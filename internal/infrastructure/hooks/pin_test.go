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

	out, err := runShim(t, body, binDir)
	if err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "hook pins") {
		t.Errorf("matching versions should print nothing, got %q", out)
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
