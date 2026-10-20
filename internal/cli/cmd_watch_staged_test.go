package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/attach"
)

func TestWatchStagedInvocation(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"extra"}} {
		var out, errb bytes.Buffer
		if code := cmdWatch(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	repoWithConfig(t, "commands: { test: 'true' }\n")
	// Live mode on the host has no single evaluation to run once.
	if code := cmdWatch([]string{"--once"}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("host --once: exit %d", code)
	}
	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "predictive_cache") {
		t.Fatalf("%d %s", code, errb.String())
	}
}

func TestPredictionReporter(t *testing.T) {
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	reporter := &predictionReporter{ctx: ctx, w: &out}
	reporter.OnStep(application.StepEvent{Phase: application.StepStarted})
	reporter.OnStep(application.StepEvent{Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepPass, Summary: "test (cached)"}})
	cancel()
	reporter.OnStep(application.StepEvent{Phase: application.StepFinished, Result: domain.StepResult{Summary: "obsolete"}})
	if !strings.Contains(out.String(), "cached") || strings.Contains(out.String(), "obsolete") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestWatchStagedOnceAndLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake runtime")
	}
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, `execution: {image: checks:local, runtime: docker}
predictive_cache: [test]
steps: {pre_commit: [test]}
commands: {test: 'true'}
`)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 {
		t.Fatalf("first evaluation: %d %s %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "prediction saved") {
		t.Fatalf("prediction not reported: %s", out.String())
	}
	out.Reset()
	errb.Reset()
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "cached") {
		t.Fatalf("repeat: %d %s %s", code, out.String(), errb.String())
	}
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out.Reset()
	errb.Reset()
	if code := runStagedWatch(ctx, svc, dir, false, &out, &errb); code != 0 || !strings.Contains(out.String(), "stopped") {
		t.Fatalf("watch loop: %d %s %s", code, out.String(), errb.String())
	}
}

// fakeDocker configures a fake Docker CLI. With engineUp set, `info` succeeds
// only while that file exists (engine down until the test creates it). With
// started set, `run` touches that file before sleeping for runSeconds.
// With imageID set, `image inspect` answers the config ID that file holds, so
// a test can retag the configured image.
type fakeDocker struct {
	engineUp   string
	started    string
	runSeconds int
	imageID    string
	// failRun makes every check fail, printing a line that looks like a
	// compiler diagnostic. warden must pass it through, not parse it.
	failRun bool
}

func installFakeDocker(t *testing.T, f fakeDocker) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake runtime")
	}
	bin := t.TempDir()
	imageID := "sha256:" + strings.Repeat("a", 64)
	// `info --format` answers the security options, which is how discovery
	// reads an engine's rootless mode.
	info := `echo '["name=seccomp,profile=builtin"]'`
	if f.engineUp != "" {
		info = "[ -f '" + f.engineUp + "' ] || exit 1; " + info
	}
	image := imageID
	if f.imageID != "" {
		image = "$(cat '" + f.imageID + "')"
	}
	run := "echo checked; exit 0"
	if f.failRun {
		run = "echo 'main.go:3: error: boom' >&2; exit 1"
	}
	if f.started != "" {
		run = "touch '" + f.started + "'; sleep " + fmt.Sprint(f.runSeconds) + "; echo checked; exit 0"
	}
	script := `#!/bin/sh
case "$1" in
info) ` + info + `;;
image) printf '%s' '[{"Id":"'"` + image + `"'","Os":"linux","Architecture":"arm64"}]';;
run) ` + run + `;;
rm) exit 0;;
*) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func stageFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
}

const stagedWatchConfig = `execution: {image: checks:local, runtime: docker}
predictive_cache: [test]
steps: {pre_commit: [test]}
commands: {test: 'true'}
`

// startStagedWatch runs the staged loop in the background with fast retry
// bounds and returns its output buffers and a stop function reporting the
// exit code.
func startStagedWatch(t *testing.T, dir string) (stdout, stderr *lockedBuffer, stop func() int) {
	t.Helper()
	minRetry, maxRetry := stagedRetryMin, stagedRetryMax
	stagedRetryMin, stagedRetryMax = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { stagedRetryMin, stagedRetryMax = minRetry, maxRetry })
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	stdout, stderr = &lockedBuffer{}, &lockedBuffer{}
	code := make(chan int, 1)
	go func() { code <- runStagedWatch(ctx, svc, dir, false, stdout, stderr) }()
	stopped := false
	stop = func() int {
		if stopped {
			return 0
		}
		stopped = true
		cancel()
		select {
		case c := <-code:
			return c
		case <-time.After(30 * time.Second):
			t.Fatal("staged watch did not stop")
			return -1
		}
	}
	t.Cleanup(func() { stop() })
	return stdout, stderr, stop
}

// After a commit the index matches HEAD. The watcher used to run a full
// container evaluation of that empty diff; it must record idle and run nothing.
func TestWatchStagedIdleWhenNothingStaged(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	started := filepath.Join(t.TempDir(), "started")
	installFakeDocker(t, fakeDocker{started: started})
	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "nothing staged") {
		t.Fatalf("once: %d %s %s", code, out.String(), errb.String())
	}
	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "idle", func() bool { return strings.Contains(stdout.String(), "nothing staged") })
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatal("a check ran although nothing was staged")
	}
}

// A stopped container engine is the machine, not the change. The snapshot must
// be retried until the engine is back, then evaluated, instead of being
// recorded as failed and left alone until the index changes.
func TestWatchStagedRetriesUntilEngineIsUp(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	engineUp := filepath.Join(t.TempDir(), "engine-up")
	installFakeDocker(t, fakeDocker{engineUp: engineUp})
	stageFile(t, dir, "sample", "staged\n")

	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != exitEnvironment || !strings.Contains(errb.String(), "blocked: container execution unavailable") {
		t.Fatalf("once with engine down: %d %s %s", code, out.String(), errb.String())
	}

	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "a retried blocked evaluation", func() bool {
		return strings.Count(stderr.String(), "blocked: container execution unavailable") >= 2
	})
	if strings.Contains(stdout.String(), "passed") {
		t.Fatalf("passed with the engine down: %s", stdout.String())
	}
	if err := os.WriteFile(engineUp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the evaluation after the engine came up", func() bool {
		return strings.Contains(stdout.String(), "staged checks passed")
	})
	if !strings.Contains(stdout.String(), "prediction saved") {
		t.Fatalf("no prediction saved after recovery: %s", stdout.String())
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}

func indexDigest(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The watcher must never contend with the developer's own git: a commit made
// while a background check is running succeeds, and the watcher leaves the
// index bytes exactly as git wrote them, before and after the commit.
func TestWatchStagedCommitDuringCheck(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	started := filepath.Join(t.TempDir(), "started")
	installFakeDocker(t, fakeDocker{started: started, runSeconds: 3})
	stageFile(t, dir, "sample", "staged\n")

	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the check to start", func() bool {
		_, err := os.Stat(started)
		return err == nil
	})
	before := indexDigest(t, dir)
	time.Sleep(300 * time.Millisecond)
	if indexDigest(t, dir) != before {
		t.Fatal("the watcher rewrote the index mid-check")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatal("index.lock held while the watcher checks")
	}
	gitIn(t, dir, "commit", "-m", "commit while the watcher checks")
	committed := indexDigest(t, dir)
	waitFor(t, 20*time.Second, "idle after the commit", func() bool {
		return strings.Contains(stdout.String(), "nothing staged")
	})
	if indexDigest(t, dir) != committed {
		t.Fatal("the watcher changed the index after the commit")
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}

// predictedImageIDs returns the image ID each saved prediction is bound to.
func predictedImageIDs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".git", "warden", "predictions", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var p domain.Prediction
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		ids[p.Contract.Environment.ImageID] = true
	}
	return ids
}

// Rebuilding or retagging the configured image moves the image ID a prediction
// is bound to. The watcher used to fingerprint only the index and the
// configuration, so it never re-evaluated and its last line kept reporting a
// pass for an image that was no longer the one a commit would run. The
// environment is now part of the snapshot: the move is reported, the staged
// inputs are evaluated again under the new image, and a prediction bound to
// the new ID is saved.
func TestWatchStagedReEvaluatesWhenTheImageMoves(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	ttl := environmentSampleTTL
	environmentSampleTTL = 50 * time.Millisecond
	t.Cleanup(func() { environmentSampleTTL = ttl })
	dir := repoWithConfig(t, stagedWatchConfig)
	oldID, newID := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	idFile := filepath.Join(t.TempDir(), "image-id")
	if err := os.WriteFile(idFile, []byte(oldID), 0o600); err != nil {
		t.Fatal(err)
	}
	installFakeDocker(t, fakeDocker{imageID: idFile})
	stageFile(t, dir, "sample", "staged\n")

	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the first evaluation", func() bool {
		return strings.Contains(stdout.String(), "staged checks passed")
	})
	for _, want := range []string{
		"warden: environment: runtime docker, image checks:local (" + oldID + "), platform linux/arm64",
		"    in runtime docker, image checks:local (" + oldID + "), platform linux/arm64",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q:\n%s", want, stdout.String())
		}
	}
	if err := os.WriteFile(idFile, []byte(newID), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the evaluation under the retagged image", func() bool {
		return strings.Count(stdout.String(), "staged checks passed") >= 2
	})
	out := stdout.String()
	if !strings.Contains(out, "warden: environment changed: runtime docker, image checks:local ("+newID+")") {
		t.Fatalf("retag not reported:\n%s", out)
	}
	if !strings.Contains(out, "    in runtime docker, image checks:local ("+newID+")") {
		t.Fatalf("re-evaluation did not run under the new image:\n%s", out)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if ids := predictedImageIDs(t, dir); !ids[newID] {
		t.Fatalf("no prediction bound to the retagged image: %v", ids)
	}
}

// A gate run in this worktree publishes on the attach socket. While it does,
// the watcher must not start container work of its own, and once it ends the
// staged inputs are evaluated.
func TestWatchStagedYieldsToAGateRun(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	started := filepath.Join(t.TempDir(), "started")
	installFakeDocker(t, fakeDocker{started: started})
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	gate := attach.NewServer(gitDir)
	if gate == nil {
		t.Fatal("could not publish a gate socket")
	}
	defer gate.Close()
	stageFile(t, dir, "sample", "staged\n")

	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the watcher to yield", func() bool {
		return strings.Contains(stdout.String(), "a gate run is in progress")
	})
	time.Sleep(time.Second)
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatalf("a check ran while the gate was running:\n%s", stdout.String())
	}
	gate.Close()
	waitFor(t, 20*time.Second, "the evaluation after the gate run", func() bool {
		return strings.Contains(stdout.String(), "staged checks passed")
	})
	if !strings.Contains(stdout.String(), "background checks resumed") {
		t.Fatalf("resume not reported:\n%s", stdout.String())
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}
