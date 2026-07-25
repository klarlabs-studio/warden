package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestMissingTool_RecognizesEveryShellsWording(t *testing.T) {
	// Each shell words the same diagnostic differently; all of them mean the
	// command never ran.
	cases := map[string]string{
		"sh: astro: command not found":                   "astro",
		"sh: 1: astro: not found":                        "astro",
		"sh: astro: not found":                           "astro",
		"bash: line 1: golangci-lint: command not found": "golangci-lint",
		"zsh: command not found: pnpm":                   "pnpm",
		"> astro build\nsh: astro: command not found\n":  "astro",
	}
	for out, want := range cases {
		got, ok := missingTool(out)
		if !ok {
			t.Errorf("missingTool(%q) = not detected, want %q", out, want)
			continue
		}
		if got != want {
			t.Errorf("missingTool(%q) = %q, want %q", out, got, want)
		}
	}
}

// The narrow shape is the whole point: a build that fails on its merits must
// never be relabelled an environment problem, which would tell the developer to
// run an install that cannot help and hide a real error.
func TestMissingTool_IgnoresProgramOutputThatMerelySaysNotFound(t *testing.T) {
	notMissing := []string{
		"",
		"main.go:12:2: undefined: foo",
		`FAIL: TestX: expected "command not found", got ""`,
		"Error: config file not found",
		"404 page not found",
		"error: the file src/x.ts was not found",
		// A test asserting on the phrase, printed by the test binary itself.
		"    got: sh: astro: command not found (quoted mid-line)",
	}
	for _, out := range notMissing {
		if tool, ok := missingTool(out); ok {
			t.Errorf("missingTool(%q) = %q, want no detection", out, tool)
		}
	}
}

func TestDetectEnvFailure_NamesTheLockfilesInstallCommand(t *testing.T) {
	cases := []struct {
		lockfile string
		want     string
	}{
		{"package-lock.json", "npm ci"},
		{"pnpm-lock.yaml", "pnpm install --frozen-lockfile"},
		{"yarn.lock", "yarn install --immutable"},
		{"bun.lockb", "bun install --frozen-lockfile"},
		// No lockfile at all: still installable, just not reproducibly.
		{"", "npm install"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		writeFileAt(t, filepath.Join(dir, "package.json"), "{}")
		if tc.lockfile != "" {
			writeFileAt(t, filepath.Join(dir, tc.lockfile), "")
		}
		fail, ok := detectEnvFailure("sh: astro: command not found", dir)
		if !ok {
			t.Fatalf("%s: not detected as an environment failure", tc.lockfile)
		}
		if fail.Remediation != tc.want {
			t.Errorf("%s: remediation = %q, want %q", tc.lockfile, fail.Remediation, tc.want)
		}
	}
}

// A monorepo scopes its step with `cd web && …`, so the package.json that needs
// installing is a child of the worktree, not the worktree itself.
func TestDetectEnvFailure_FindsANestedPackage(t *testing.T) {
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileAt(t, filepath.Join(web, "package.json"), "{}")
	writeFileAt(t, filepath.Join(web, "pnpm-lock.yaml"), "")

	fail, ok := detectEnvFailure("sh: astro: command not found", dir)
	if !ok {
		t.Fatal("not detected as an environment failure")
	}
	if !strings.Contains(fail.Remediation, "pnpm install") || !strings.Contains(fail.Remediation, "web") {
		t.Errorf("remediation = %q, want the pnpm install scoped to web", fail.Remediation)
	}
}

// When node_modules IS present the tool is genuinely absent from the machine, so
// "run npm ci" would be wrong advice. Warden says nothing rather than mislead.
func TestInstallHint_WithheldWhenDepsAreInstalled(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "package.json"), "{}")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	fail, ok := detectEnvFailure("sh: astro: command not found", dir)
	if !ok {
		t.Fatal("still an environment failure — the tool is missing")
	}
	if fail.Remediation != "" {
		t.Errorf("remediation = %q, want none: deps are installed, so a reinstall is not the fix", fail.Remediation)
	}
}

// The reported reason is the whole point of #91: an absent toolchain is not a
// broken build, and the message must say so and name the fix.
func TestShellStep_MissingToolchainIsNotABuildFailure(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "package.json"), "{}")
	writeFileAt(t, filepath.Join(dir, "package-lock.json"), "")

	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"js-build": "astro build"},
	}
	res, err := NewShellStep("js-build", "js-build").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	// The gate still fails: an unbuilt tree is not a validated tree.
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if res.Blocker != domain.BlockerEnvironment {
		t.Errorf("Blocker = %q, want %q", res.Blocker, domain.BlockerEnvironment)
	}
	if !strings.Contains(res.Summary, "could not run") {
		t.Errorf("Summary = %q, want it to say the step never ran", res.Summary)
	}
	msg := res.Findings[0].Message
	for _, want := range []string{
		"astro is not installed",
		"not a problem with your change",
		"npm ci",
		"NODE_AUTH_TOKEN",
		"npm config set",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

// The credential guidance is the half of #91 that matters: the message must not
// merely say "install deps", because the obvious way to authenticate that
// install writes a live token into a tracked .npmrc.
func TestEnvFailureMessage_WarnsAgainstNpmConfigSet(t *testing.T) {
	msg := envFailure{Tool: "astro", Remediation: "npm ci"}.message("js-build")
	if !strings.Contains(msg, "do NOT run `npm config set") {
		t.Errorf("message must steer away from the token-leaking fix:\n%s", msg)
	}
	if !strings.Contains(msg, ".npmrc, a file most repos track") {
		t.Errorf("message must say WHY npm config set is dangerous:\n%s", msg)
	}
}

// A genuine build failure must keep reading as one — no blocker, no install
// advice. This is the guard that keeps the detector honest.
func TestShellStep_RealBuildFailureCarriesNoBlocker(t *testing.T) {
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"js-build": `echo "src/app.ts(3,1): error TS2304: Cannot find name 'foo'."; exit 2`},
	}
	res, err := NewShellStep("js-build", "js-build").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if res.Blocker != domain.BlockerNone {
		t.Errorf("Blocker = %q, want none — the build ran and rejected the change", res.Blocker)
	}
	if res.Summary != "js-build failed" {
		t.Errorf("Summary = %q, want the ordinary failure wording", res.Summary)
	}
}

// "Cannot find module" is the same missing-node_modules cause arriving one layer
// later, when the runner starts but cannot resolve what it needs.
func TestShellStep_UnresolvedModuleIsAnEnvironmentFailure(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "package.json"), "{}")
	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"js-check": `echo "Error: Cannot find module 'vitest'" >&2; exit 1`},
	}
	res, err := NewShellStep("js-check", "js-check").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocker != domain.BlockerEnvironment {
		t.Errorf("Blocker = %q, want %q", res.Blocker, domain.BlockerEnvironment)
	}
	if !strings.Contains(res.Findings[0].Message, "dependencies are not installed") {
		t.Errorf("message should name the missing deps:\n%s", res.Findings[0].Message)
	}
}

// Contention keeps its own blocker: both are environmental, but only one clears
// by waiting, and the exit codes downstream depend on the difference.
func TestShellStep_ContentionCarriesTheContentionBlocker(t *testing.T) {
	origBudget, origPoll := contentionBudget, contentionPoll
	contentionBudget, contentionPoll = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { contentionBudget, contentionPoll = origBudget, origPoll })

	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": `echo "Error: parallel golangci-lint is running" >&2; exit 1`},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocker != domain.BlockerContention {
		t.Errorf("Blocker = %q, want %q", res.Blocker, domain.BlockerContention)
	}
	if !res.Blocker.Retryable() {
		t.Error("contention must be retryable — that is what distinguishes it")
	}
}

// The permanent cure for #90 is only correct because warden isolates the cache
// the lock protects, so only warden is in a position to recommend it.
func TestParallelRunnerHint(t *testing.T) {
	hint := parallelRunnerHint("golangci-lint run ./...")
	if !strings.Contains(hint, "--allow-parallel-runners") {
		t.Errorf("hint = %q, want the flag that removes the lock", hint)
	}
	if !strings.Contains(hint, "GOLANGCI_LINT_CACHE") {
		t.Errorf("hint = %q, want the reason the flag is safe here", hint)
	}
	// Already taken the advice, or not golangci-lint at all: stay quiet.
	for _, cmd := range []string{
		"golangci-lint run --allow-parallel-runners ./...",
		"golangci-lint run --allow-serial-runners ./...",
		"cargo clippy",
		"make lint",
	} {
		if got := parallelRunnerHint(cmd); got != "" {
			t.Errorf("parallelRunnerHint(%q) = %q, want silence", cmd, got)
		}
	}
}

func TestShellStep_ContentionMessageOffersTheParallelRunnersFlag(t *testing.T) {
	origBudget, origPoll := contentionBudget, contentionPoll
	contentionBudget, contentionPoll = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { contentionBudget, contentionPoll = origBudget, origPoll })

	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands: map[string]string{
			"lint": `echo "Error: parallel golangci-lint is running" >&2; exit 1`,
		},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Findings[0].Message, "--allow-parallel-runners") {
		t.Errorf("a repeat offender should be told how to stop it recurring:\n%s", res.Findings[0].Message)
	}
}

// writeFileAt writes content to an absolute path. Named to avoid colliding with
// security_scan_test.go's writeFileAt(t, dir, name, body), which joins instead.
func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
