package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestMissingCommand(t *testing.T) {
	cases := map[string]string{
		"sh: astro: command not found":            "astro",
		"bash: line 1: astro: command not found":  "astro",
		"zsh: command not found: astro":           "astro",
		"sh: 1: vite: not found":                  "vite",
		"sh: golangci-lint: command not found":    "golangci-lint",
		"main.go:1:1: undefined: foo":             "",
		"FAIL\tpkg\t0.4s":                         "",
		"error: the file was not found in the db": "",
	}
	for out, want := range cases {
		if got := missingCommand(out); got != want {
			t.Errorf("missingCommand(%q) = %q, want %q", out, got, want)
		}
	}
}

// The distinguishing signal: a manifest says dependencies are expected, and the
// directory they land in is absent. That is an environment nobody installed,
// not a broken build.
func TestClassifyEnvFailureUninstalledDependencies(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"x"}`)
	write(t, dir, "pnpm-lock.yaml", "lockfileVersion: 9")

	env := classifyEnvFailure("js-build", "sh: astro: command not found", dir)
	if env == nil {
		t.Fatal("a missing command with an uninstalled manifest must classify")
	}
	if !strings.Contains(env.Summary, "dependencies not installed") {
		t.Errorf("Summary = %q", env.Summary)
	}
	// The most specific lockfile wins, so the remediation is the right one.
	if !strings.Contains(env.Message, "pnpm install") {
		t.Errorf("Message should name pnpm: %q", env.Message)
	}
	if !strings.Contains(env.Message, "Nothing is wrong with your tree") {
		t.Errorf("the developer must not be sent to their diff: %q", env.Message)
	}
}

func TestClassifyEnvFailureLockfilePicksTheInstaller(t *testing.T) {
	cases := map[string]string{
		"package-lock.json": "npm ci",
		"yarn.lock":         "yarn install",
		"bun.lockb":         "bun install",
	}
	for lock, want := range cases {
		t.Run(lock, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "package.json", "{}")
			write(t, dir, lock, "x")
			env := classifyEnvFailure("js-check", "sh: vite: command not found", dir)
			if env == nil || !strings.Contains(env.Message, want) {
				t.Errorf("want %q in remediation, got %+v", want, env)
			}
		})
	}
	// No lockfile at all still gets a usable instruction.
	dir := t.TempDir()
	write(t, dir, "package.json", "{}")
	if env := classifyEnvFailure("js-check", "sh: vite: command not found", dir); env == nil ||
		!strings.Contains(env.Message, "npm install") {
		t.Errorf("a lockfile-less project should still be told how to install: %+v", env)
	}
}

// Dependencies present means the missing command is something else — the fix is
// installing a tool, not running an installer.
func TestClassifyEnvFailureToolNotInstalled(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", "{}")
	write(t, dir, "package-lock.json", "{}")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := classifyEnvFailure("lint", "sh: golangci-lint: command not found", dir)
	if env == nil {
		t.Fatal("a missing command must still classify")
	}
	if strings.Contains(env.Summary, "dependencies not installed") {
		t.Errorf("deps ARE installed; this is a missing tool: %q", env.Summary)
	}
	if !strings.Contains(env.Summary, "golangci-lint not installed") {
		t.Errorf("Summary = %q", env.Summary)
	}
}

// A real failure must never be softened into an environment excuse.
func TestClassifyEnvFailureIgnoresGenuineFailures(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", "{}")
	for _, out := range []string{
		"main.go:12:2: undefined: foo",
		"FAIL\tgo.klarlabs.de/warden/internal/cli\t0.4s",
		"Error: build failed with 3 errors",
	} {
		if env := classifyEnvFailure("test", out, dir); env != nil {
			t.Errorf("classifyEnvFailure(%q) = %+v, want nil", out, env)
		}
	}
}

// End to end through the shell step: a missing command still FAILS the gate —
// "I could not run" is not "the tree is clean" — but says what is actually wrong.
func TestShellStep_MissingCommandReportsTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"x"}`)
	write(t, dir, "package-lock.json", "{}")

	res, err := NewShellStep("js-build", "js-build").Run(context.Background(), application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"js-build": "astro build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail — an unrun check is not a pass", res.Status)
	}
	if !strings.Contains(res.Summary, "dependencies not installed") {
		t.Errorf("Summary = %q, want it to name the environment", res.Summary)
	}
	if !strings.Contains(res.Findings[0].Message, "npm ci") {
		t.Errorf("the finding must carry the remediation: %q", res.Findings[0].Message)
	}
	// The tool's own output survives, so nothing is hidden.
	if !strings.Contains(res.Findings[0].Message, "not found") {
		t.Errorf("the original output must still be present: %q", res.Findings[0].Message)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
