// Package e2e drives the built warden binary against real git repositories,
// exercising the whole gate end to end: the pre-commit fast path, the pre-push
// pipeline with its push and provenance notes, and the doctor bypass audit.
// Opt-in via WARDEN_E2E=1 (see Makefile `make e2e`); a plain `go test ./...`
// skips it. An env gate rather than a build tag keeps the package always
// listable by go list / coverage tooling.
//
// ALWAYS RUN THESE WITH -count=1.
//
// These tests exercise a separately built binary, so from the go tool's point of
// view this package depends only on os/exec/strings/testing. A change anywhere
// in internal/** leaves the test binary's inputs identical, Go serves the run
// from its test cache, and the suite reports a stale PASS without executing.
// That is not theoretical: the "End-to-End" CI check reported success as
// `ok go.klarlabs.de/warden/e2e (cached)` while the push-contract test below was
// actually failing, which is how a pre-push regression reached main behind a
// green gate. Both invocations (Makefile, ci.yml) now pass -count=1; keep it.
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
		_ = os.RemoveAll(dir)
		panic("build warden: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir) // os.Exit skips defers, so clean up explicitly
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
	return h.wardenIn("", args...)
}

// wardenIn runs the binary with stdin fed from a string — used to replay the
// pushed-ref list git hands a pre-push hook on stdin.
func (h *harness) wardenIn(stdin string, args ...string) (string, int) {
	h.t.Helper()
	cmd := exec.Command(wardenBin, args...)
	cmd.Dir = h.dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("warden %v: %v", args, err)
	}
	return string(out), code
}

// hookEnv puts the binary under test at the FRONT of PATH. An installed hook
// shim resolves `warden` from PATH, so without this a real `git push` would
// gate through whatever warden the developer (or the runner image) happens to
// have installed — testing a released binary instead of this working tree, and
// reporting its behavior as if it were ours.
func hookEnv() []string {
	return append(os.Environ(), "PATH="+filepath.Dir(wardenBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gitTry runs git and returns (combined output, exit code) instead of failing
// the test, for the cases where the exit code IS the assertion. It runs with
// hookEnv so any hook git fires is the binary under test.
func (h *harness) gitTry(args ...string) (string, int) {
	h.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = h.dir
	cmd.Env = hookEnv()
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("git %v: %v", args, err)
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

	// Drive a REAL `git push` through the installed hook rather than invoking
	// `warden run pre-push` directly. Who performs the push is now the thing under
	// test, and only a real push exercises it: git resolves its refs before the
	// hook runs, so the hand-off can only be observed from git's side.
	out, code := h.gitTry("push", "origin", "main")

	// Nothing rewrote the commit, so warden stands aside and git performs the
	// push. That is what makes a passing push exit 0 (#89) — the old contract
	// (always non-zero, plus "error: failed to push some refs") trained everyone
	// to ignore this tool's errors.
	if code != 0 {
		t.Fatalf("a passing push must exit 0, got %d: %s", code, out)
	}
	if strings.Contains(out, "error:") {
		t.Errorf("a passing push must not print an error: %s", out)
	}
	if !strings.Contains(out, "git is completing the push") {
		t.Errorf("expected the hand-off message, got: %s", out)
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

// TestE2E_WardenPerformedPushExitsThree pins the other half of the exit
// contract. When a step REWRITES the branch, warden performs the push itself and
// must then fail the hook on purpose so git's now-stale compare-and-swap cannot
// race it. That case is a success — the commits are on the remote — but it used
// to exit 1, the same code as "the gate rejected your change", making the most
// common successful outcome indistinguishable from a rejection for every wrapper,
// CI step and agent reading the status. It now exits 3.
//
// This is the sibling of TestE2E_PrePushSelfPushWhenStepRewrites, not a
// duplicate of it: that one drives a real `git push` and asserts what GIT
// reports (non-zero, because git only distinguishes zero from non-zero and
// substitutes its own 1); this one calls the hook entry point directly and
// asserts what WARDEN returns, which is the code wrappers and agents read.
func TestE2E_WardenPerformedPushExitsThree(t *testing.T) {
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

	// A `writes:` step runs as a sequential barrier in the shared worktree with its
	// changes KEPT. Committing there moves the worktree HEAD away from the seed
	// tip, which is exactly the condition under which warden must perform the push
	// itself (delegateToGit requires finalSHA == seedTip) — the real-world shape
	// being an auto-fix or an amending agent step.
	h.write(".warden.yaml", `
hooks: { pre_push: true }
commands: { amend: "printf 'fixed\n' > a.txt && git commit --no-verify -am 'auto-fixed by a step'" }
steps: { pre_push: [amend] }
writes: [amend]
rules: []
`)
	h.warden("init")
	h.write("a.txt", "feature\n")
	h.git("commit", "--no-verify", "-am", "feature change")

	// Invoke the hook entry point directly, because THAT is the exit code this
	// change is about. Git does not propagate a hook's status: it observes only
	// zero vs non-zero and then reports its own failure as 1. So the audience for
	// the distinct code is everything that calls warden directly — retry wrappers,
	// CI steps, and the agent surfaces — not the interactive `git push` user, who
	// gets git's 1 either way and reads the printed explanation instead.
	out, code := h.warden("run", "pre-push")

	if code != 3 {
		t.Fatalf("a push warden performed itself must exit 3 (passed, warden pushed), got %d: %s", code, out)
	}
	// The success must be legible in the output too, not only in the code.
	if !strings.Contains(out, "exit 3") {
		t.Errorf("expected the heads-up naming exit 3, got: %s", out)
	}
	// The commits must actually be on the remote — the whole reason this non-zero
	// exit is a success and not a failure.
	if remoteSHA := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main")); remoteSHA == "" {
		t.Error("remote main is empty; warden did not perform the push")
	}

	// A rejection must NOT share that code — the point of the split.
	h.write(".warden.yaml", `
hooks: { pre_push: true }
commands: { lint: "echo nope >&2; exit 1" }
steps: { pre_push: [lint] }
rules: []
`)
	h.git("commit", "--no-verify", "-am", "another change")
	if _, rejectCode := h.warden("run", "pre-push"); rejectCode == 3 {
		t.Error("a rejected push must not exit 3; that code means the push succeeded")
	}
}

// TestE2E_PrePushSkipsNonBranchPush pins the fix for a notes/tag push needlessly
// re-running the gate: when git's pre-push stdin advances no branch (here a
// refs/notes/warden push), warden exits 0 without running the pipeline, letting
// git complete the push itself.
func TestE2E_PrePushSkipsNonBranchPush(t *testing.T) {
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
	h.write(".warden.yaml", cfgLintTestPass)
	h.warden("init")

	// A notes-only push: git would feed a pre-push hook a single ref line whose
	// remote ref is refs/notes/warden — no branch is advanced.
	const zero = "0000000000000000000000000000000000000000"
	stdin := "refs/notes/warden abc123 refs/notes/warden " + zero + "\n"
	out, code := h.wardenIn(stdin, "run", "pre-push")
	if code != 0 {
		t.Fatalf("notes-only push must not gate; got exit %d: %s", code, out)
	}
	if !strings.Contains(out, "nothing to gate") {
		t.Fatalf("expected the skip message, got: %s", out)
	}
}

func TestE2E_DoctorDetectsBypass(t *testing.T) {
	remote := t.TempDir()
	_ = exec.Command("git", "init", "--bare", remote).Run()
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

// TestE2E_PrePushSelfPushWhenStepRewrites pins the other half of the push
// contract. When a step rewrites the commit, git holds the pre-rewrite sha and
// its push protocol is a compare-and-swap, so its attempt would be rejected —
// warden pushes the validated commit itself and fails the hook on purpose to
// pre-empt it. That exit MUST stay non-zero: it is the only thing stopping git
// from racing a stale ref onto the remote.
func TestE2E_PrePushSelfPushWhenStepRewrites(t *testing.T) {
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

	// A step that commits in the worktree moves the tip off the sha git resolved.
	h.write(".warden.yaml", `
hooks: { pre_commit: true, pre_push: true }
commands: { amender: "git commit -q --allow-empty --no-verify -m warden-added-this" }
steps: { pre_commit: [], pre_push: [amender] }
rules: []
`)
	h.warden("init")
	h.write("a.txt", "feature\n")
	h.git("commit", "--no-verify", "-am", "feature change")
	before := h.git("rev-parse", "HEAD")

	out, code := h.gitTry("push", "origin", "main")
	if code == 0 {
		t.Fatalf("a rewriting push must fail the hook so git cannot race a stale ref: %s", out)
	}
	after := h.git("rev-parse", "HEAD")
	if after == before {
		t.Fatalf("expected the step to rewrite HEAD; it did not: %s", out)
	}
	// The success line must name what landed — the #89 complaint was that you
	// could not tell a successful push from a failed one without git ls-remote.
	if !strings.Contains(out, "pushed "+after[:12]) {
		t.Errorf("expected the pushed sha in the message, got: %s", out)
	}
	if remoteSHA := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main")); remoteSHA != after {
		t.Errorf("remote main = %s, want the validated commit %s", remoteSHA, after)
	}
}
