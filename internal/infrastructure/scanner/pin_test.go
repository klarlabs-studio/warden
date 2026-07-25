package scanner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoWithWorkflows writes .github/workflows/<name> files into a temp repo.
func repoWithWorkflows(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDiscoverPin(t *testing.T) {
	t.Run("a workflow-level env pin", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{"ci.yml": `
name: CI
env:
  NOX_VERSION: "1.3.0"
jobs:
  security:
    runs-on: ubuntu-latest
`})
		pin, found, err := DiscoverPin(context.Background(), root, "nox", "")
		if err != nil || !found {
			t.Fatalf("DiscoverPin = (found %v, err %v), want a pin", found, err)
		}
		if pin.Version != "1.3.0" {
			t.Errorf("version = %q, want 1.3.0", pin.Version)
		}
		if pin.Source != ".github/workflows/ci.yml" {
			t.Errorf("source = %q, want the workflow that carries the pin", pin.Source)
		}
	})

	t.Run("a step-level env pin", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{"ci.yml": `
jobs:
  security:
    steps:
      - name: Scan
        env:
          NOX_VERSION: v1.15.0
        run: nox scan .
`})
		pin, found, _ := DiscoverPin(context.Background(), root, "nox", "")
		if !found || pin.Version != "1.15.0" {
			t.Errorf("DiscoverPin = %+v (found %v), want 1.15.0 — a `v` prefix is the same pin", pin, found)
		}
	})

	t.Run("a reusable-workflow input", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{"ci.yml": `
jobs:
  ci:
    uses: org/.github/.github/workflows/go-ci.yml@main
    with:
      nox-version: "1.15.0"
`})
		pin, found, _ := DiscoverPin(context.Background(), root, "nox", "")
		if !found || pin.Version != "1.15.0" {
			t.Errorf("DiscoverPin = %+v (found %v), want the reusable-workflow input to count as the pin", pin, found)
		}
	})

	t.Run("no pin is silence, not an error", func(t *testing.T) {
		// Most repos pin nothing. Warden must not fail their pushes over a
		// comparison it cannot make.
		root := repoWithWorkflows(t, map[string]string{"ci.yml": "jobs:\n  build:\n    runs-on: ubuntu-latest\n"})
		if _, found, err := DiscoverPin(context.Background(), root, "nox", ""); found || err != nil {
			t.Errorf("DiscoverPin = (found %v, err %v), want (false, nil)", found, err)
		}
	})

	t.Run("no workflows at all is silence", func(t *testing.T) {
		if _, found, err := DiscoverPin(context.Background(), t.TempDir(), "nox", ""); found || err != nil {
			t.Errorf("DiscoverPin = (found %v, err %v), want (false, nil)", found, err)
		}
	})

	t.Run("`latest` names no version", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{"ci.yml": "env:\n  NOX_VERSION: latest\n"})
		if _, found, _ := DiscoverPin(context.Background(), root, "nox", ""); found {
			t.Error("`latest` was treated as a pin; there is no version to compare against")
		}
	})

	t.Run("two workflows pinning different versions is itself the drift", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{
			"ci.yml":      "env:\n  NOX_VERSION: \"1.3.0\"\n",
			"release.yml": "env:\n  NOX_VERSION: \"1.15.0\"\n",
		})
		_, _, err := DiscoverPin(context.Background(), root, "nox", "")
		if err == nil {
			t.Fatal("conflicting pins were silently resolved to one of them")
		}
		if !strings.Contains(err.Error(), "1.3.0") || !strings.Contains(err.Error(), "1.15.0") {
			t.Errorf("error %q must name both versions so the operator can see which to fix", err)
		}
	})

	t.Run("the same pin in two workflows agrees", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{
			"ci.yml":      "env:\n  NOX_VERSION: \"1.15.0\"\n",
			"release.yml": "env:\n  NOX_VERSION: v1.15.0\n",
		})
		if _, found, err := DiscoverPin(context.Background(), root, "nox", ""); err != nil || !found {
			t.Errorf("DiscoverPin = (found %v, err %v), want the agreed pin", found, err)
		}
	})

	t.Run("an explicit pin_file that does not exist is an error", func(t *testing.T) {
		// A silently skipped check is worse than a loud one: the operator asked
		// for the pin to be read from a specific file.
		if _, _, err := DiscoverPin(context.Background(), t.TempDir(), "nox", ".github/workflows/nope.yml"); err == nil {
			t.Error("a missing pin_file was ignored")
		}
	})

	t.Run("pin_file narrows the search", func(t *testing.T) {
		root := repoWithWorkflows(t, map[string]string{
			"ci.yml":      "env:\n  NOX_VERSION: \"1.3.0\"\n",
			"release.yml": "env:\n  NOX_VERSION: \"1.15.0\"\n",
		})
		pin, found, err := DiscoverPin(context.Background(), root, "nox", ".github/workflows/release.yml")
		if err != nil || !found || pin.Version != "1.15.0" {
			t.Errorf("DiscoverPin = (%+v, %v, %v), want only release.yml's 1.15.0", pin, found, err)
		}
	})
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.15.0", "1.15.0", true},
		{"v1.15.0", "1.15.0", true},
		{"nox 1.15.0 (commit: abc, built: 2026-07-25T11:33:56Z)", "1.15.0", true},
		{"1.3.0", "1.15.0", false},
		{"1.15", "1.15.0", false},
		// Nothing to compare must never read as agreement.
		{"", "1.15.0", false},
		{"latest", "1.15.0", false},
	}
	for _, c := range cases {
		if got := SameVersion(c.a, c.b); got != c.want {
			t.Errorf("SameVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"1.15.0":                    "1.15.0",
		"v1.15.0":                   "1.15.0",
		"go install .../cli@v0.8.1": "0.8.1",
		"1.15.0-rc.1":               "1.15.0-rc.1",
		"nox 1.15.0 (commit: abc)":  "1.15.0",
		"latest":                    "",
		"":                          "",
		"${{ inputs.version }}":     "",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake scanner is a POSIX shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'nox 1.15.0 (commit: abc, built: 2026-07-25T11:33:56Z)'\n"
	if err := os.WriteFile(filepath.Join(bin, "fakescanner"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := LocalVersion(context.Background(), t.TempDir(), "fakescanner"); got != "1.15.0" {
		t.Errorf("LocalVersion = %q, want 1.15.0", got)
	}

	// A scanner that is not installed must yield "" so the drift check stays
	// quiet, rather than failing a push over a tool warden does not require.
	if got := LocalVersion(context.Background(), t.TempDir(), "definitely-not-installed-scanner"); got != "" {
		t.Errorf("LocalVersion for a missing binary = %q, want \"\"", got)
	}
}
