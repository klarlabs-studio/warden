// Package e2e drives the built warden binary against real git repositories,
// exercising the whole gate end to end: the pre-commit fast path, the pre-push
// pipeline with its self-performed push and provenance notes, and the doctor
// bypass audit. Opt-in via WARDEN_E2E=1 (see Makefile `make e2e`); a plain
// `go test ./...` skips it. An env gate rather than a build tag keeps the
// package always listable by go list / coverage tooling.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wardenBin is the path to the warden binary built once for the whole suite.
var wardenBin string

func TestMain(m *testing.M) {
	if os.Getenv("WARDEN_E2E") == "" {
		os.Exit(0) // opt-in; skip by default so `go test ./...` stays fast
	}
	if _, err := exec.LookPath("git"); err != nil {
		os.Exit(0) // no git → nothing to drive
	}
	dir, err := os.MkdirTemp("", "warden-e2e-bin-")
	if err != nil {
		panic(err)
	}

	wardenBin = filepath.Join(dir, "warden")
	build := exec.Command("go", "build", "-o", wardenBin, ".")
	build.Dir = ".." // repo root
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("build warden: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir) // os.Exit skips defers, so clean up explicitly
	os.Exit(code)
}

// harness runs git and warden in a scratch repo.
type harness struct {
	t   *testing.T
	dir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir()}
	h.git("init")
	h.git("config", "user.email", "e2e@warden.test")
	h.git("config", "user.name", "e2e")
	return h
}

func (h *harness) git(args ...string) string {
	h.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = h.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// warden runs the binary and returns (stdout+stderr, exitCode).
func (h *harness) warden(args ...string) (string, int) {
	h.t.Helper()
	cmd := exec.Command(wardenBin, args...)
	cmd.Dir = h.dir
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("warden %v: %v", args, err)
	}
	return string(out), code
}

func (h *harness) write(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

const cfgLintTestPass = `
hooks: { pre_commit: true, pre_push: true }
commands: { lint: "true", test: "true" }
steps: { pre_commit: [lint], pre_push: [rebase, test, lint] }
rules: []
`

func TestE2E_PreCommitGate(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "hello\n")
	h.git("add", "a.txt")
	h.git("commit", "--no-verify", "-m", "init")

	// Pass config: lint = true.
	h.write(".warden.yaml", cfgLintTestPass)
	if out, code := h.warden("init"); code != 0 {
		t.Fatalf("init failed (%d): %s", code, out)
	}
	h.write("a.txt", "changed\n")
	h.git("add", "a.txt")
	if out, code := h.warden("run", "pre-commit"); code != 0 {
		t.Fatalf("clean pre-commit should pass, got %d: %s", code, out)
	}

	// Fail config: lint = false must block.
	h.write(".warden.yaml", strings.Replace(cfgLintTestPass, `lint: "true"`, `lint: "false"`, 1))
	out, code := h.warden("run", "pre-commit")
	if code == 0 {
		t.Fatalf("failing lint must block pre-commit, got exit 0: %s", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("expected failure message, got: %s", out)
	}
}

func TestE2E_ConfigCommandCustomStep(t *testing.T) {
	h := newHarness(t)
	h.write("a.txt", "hello\n")
	h.git("add", "a.txt")
	h.git("commit", "--no-verify", "-m", "init")

	// A custom step "extra-check" defined purely by a command — no binary.
	cfg := `
hooks: { pre_commit: true }
commands: { lint: "true", extra-check: "echo custom-failure >&2; exit 1" }
steps: { pre_commit: [lint, extra-check] }
rules: []
`
	h.write(".warden.yaml", cfg)
	h.warden("init")
	h.write("a.txt", "changed\n")
	h.git("add", "a.txt")

	out, code := h.warden("run", "pre-commit")
	if code == 0 {
		t.Fatalf("config-command custom step should fail the gate, got exit 0: %s", out)
	}
	if !strings.Contains(out, "extra-check") {
		t.Errorf("expected the custom step name in the failure, got: %s", out)
	}
}

func TestE2E_PrePushPushesWithProvenance(t *testing.T) {
	// Bare remote + work repo.
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	h := newHarness(t)
	h.git("remote", "add", "origin", remote)
	h.write("a.txt", "hello\n")
	h.git("add", "a.txt")
	h.git("commit", "--no-verify", "-m", "init")
	h.git("branch", "-M", "main")
	h.git("push", "--no-verify", "-u", "origin", "main")

	h.write(".warden.yaml", cfgLintTestPass)
	h.warden("init")
	h.write("a.txt", "feature\n")
	h.git("commit", "--no-verify", "-am", "feature change")
	localSHA := h.git("rev-parse", "HEAD")

	out, code := h.warden("run", "pre-push")
	// Pre-push always exits non-zero (warden performed the push itself).
	if code == 0 {
		t.Fatalf("pre-push should exit non-zero after self-push: %s", out)
	}
	if !strings.Contains(out, "warden pushed") {
		t.Fatalf("expected self-push message, got: %s", out)
	}

	// The remote must now hold the feature commit.
	remoteSHA := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main"))
	if remoteSHA != localSHA {
		t.Errorf("remote main = %s, want pushed %s", remoteSHA, localSHA)
	}

	// doctor must report the commit as verified with an intact chain.
	dout, dcode := h.warden("doctor")
	if dcode != 0 {
		t.Errorf("doctor should pass with a verified commit, got %d: %s", dcode, dout)
	}
	if !strings.Contains(dout, "1 verified") || !strings.Contains(dout, "chain-intact") {
		t.Errorf("doctor did not report the verified commit: %s", dout)
	}
}

func TestE2E_DoctorDetectsBypass(t *testing.T) {
	remote := t.TempDir()
	exec.Command("git", "init", "--bare", remote).Run()
	h := newHarness(t)
	h.git("remote", "add", "origin", remote)
	h.write("a.txt", "hello\n")
	h.git("add", "a.txt")
	h.git("commit", "--no-verify", "-m", "init")
	h.git("branch", "-M", "main")
	h.git("push", "--no-verify", "-u", "origin", "main")

	h.write(".warden.yaml", cfgLintTestPass)
	h.warden("init")

	// A --no-verify push sneaks a commit past the gate.
	h.write("a.txt", "sneaky\n")
	h.git("commit", "--no-verify", "-am", "bypassed change")
	h.git("push", "--no-verify", "origin", "main")

	out, code := h.warden("doctor")
	if code == 0 {
		t.Errorf("doctor should flag the unverified commit (non-zero exit): %s", out)
	}
	if !strings.Contains(out, "UNVERIFIED") || !strings.Contains(out, "1 unverified") {
		t.Errorf("doctor did not flag the bypass: %s", out)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
