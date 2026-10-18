package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStagedFingerprintDetectsStagingWithoutFileChanges(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init")
	git("config", "user.name", "warden-watch")
	git("config", "user.email", "warden-watch")
	git("commit", "--allow-empty", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := StagedFingerprint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	after, err := StagedFingerprint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("staging did not change fingerprint")
	}
	git("reset", "--", "file")
	reset, err := StagedFingerprint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if reset == after {
		t.Fatal("unstaging did not change fingerprint")
	}
}

func TestLoopCancelsObsoleteWorkAndSerializes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var state, active, maxActive atomic.Int32
	state.Store(1)
	started := make(chan int32, 8)
	release := make(chan struct{})
	finished := make(chan struct{})
	evaluate := func(ctx context.Context) error {
		value := state.Load()
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		defer active.Add(-1)
		started <- value
		if value == 1 {
			<-ctx.Done()
			<-release
			return ctx.Err()
		}
		return nil
	}
	go func() {
		Loop{Interval: 5 * time.Millisecond, Fingerprint: func(context.Context) (string, error) { return fmt.Sprint(state.Load()), nil }, Evaluate: evaluate}.Run(ctx)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("no initial evaluation")
	}
	state.Store(2)
	// Teardown must finish before the replacement can start.
	select {
	case <-started:
		t.Fatal("overlapping evaluation")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-started:
		if got != 2 {
			t.Fatalf("evaluated %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no replacement")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not stop")
	}
	if maxActive.Load() != 1 {
		t.Fatal("overlapping evaluations")
	}
}

func gitRepo(t *testing.T) (string, func(args ...string)) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init")
	git("config", "user.name", "warden-watch")
	git("config", "user.email", "warden-watch")
	git("commit", "--allow-empty", "-m", "initial")
	return dir, git
}

func TestNothingStaged(t *testing.T) {
	dir, git := gitRepo(t)
	ctx := context.Background()
	if empty, err := NothingStaged(ctx, dir); err != nil || !empty {
		t.Fatalf("fresh repo: %v %v", empty, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, _ := NothingStaged(ctx, dir); !empty {
		t.Fatal("an unstaged file is not staged")
	}
	git("add", "file")
	if empty, err := NothingStaged(ctx, dir); err != nil || empty {
		t.Fatalf("staged file: %v %v", empty, err)
	}
	git("commit", "-m", "file")
	if empty, err := NothingStaged(ctx, dir); err != nil || !empty {
		t.Fatalf("after commit: %v %v", empty, err)
	}
	if _, err := NothingStaged(ctx, t.TempDir()); err == nil {
		t.Fatal("outside a repository must be an error, not idle")
	}
}

func TestStatGateHashesOnlyOnChangeWithBackstop(t *testing.T) {
	key := "k1"
	var full int
	gate := &StatGate{
		Key:      func(context.Context) (string, error) { return key, nil },
		Full:     func(context.Context) (string, error) { full++; return fmt.Sprint("v", full), nil },
		Backstop: 40 * time.Millisecond,
	}
	ctx := context.Background()
	first, _ := gate.Fingerprint(ctx)
	if again, _ := gate.Fingerprint(ctx); again != first || full != 1 {
		t.Fatalf("unchanged key recomputed: %s %d", again, full)
	}
	key = "k2"
	if _, _ = gate.Fingerprint(ctx); full != 2 {
		t.Fatalf("changed key not recomputed: %d", full)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _ = gate.Fingerprint(ctx); full != 3 {
		t.Fatalf("backstop did not force a recompute: %d", full)
	}
	failing := &StatGate{
		Key:  func(context.Context) (string, error) { return "", fmt.Errorf("stat") },
		Full: func(context.Context) (string, error) { full++; return "v", nil },
	}
	before := full
	_, _ = failing.Fingerprint(ctx)
	_, _ = failing.Fingerprint(ctx)
	if full != before+2 {
		t.Fatal("a key error must fall through to a full computation every time")
	}
}

func TestStagedStatKeyTracksStagingCommitsAndConfig(t *testing.T) {
	dir, git := gitRepo(t)
	ctx := context.Background()
	key, err := StagedStatKey(ctx, dir, ".warden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sample := func() string {
		t.Helper()
		v, err := key(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	base := sample()
	if sample() != base {
		t.Fatal("key not stable")
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	staged := sample()
	if staged == base {
		t.Fatal("staging did not change the key")
	}
	git("commit", "-m", "file")
	committed := sample()
	if committed == staged {
		t.Fatal("committing did not change the key")
	}
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte("commands: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sample() == committed {
		t.Fatal("configuration change did not change the key")
	}
}

// Every watcher git call carries GIT_OPTIONAL_LOCKS=0. A fake git on PATH
// records its environment.
func TestWatcherGitCallsDisableOptionalLocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake git")
	}
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\necho \"$GIT_OPTIONAL_LOCKS\" >> '" + record + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	ctx := context.Background()
	_, _ = NothingStaged(ctx, t.TempDir())
	_, _ = StagedFingerprint(ctx, t.TempDir())
	_, _ = StagedStatKey(ctx, t.TempDir())
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "0" {
			t.Fatalf("git ran with GIT_OPTIONAL_LOCKS=%q:\n%s", line, data)
		}
	}
}
