package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRunStagedCancelsObsoleteWorkAndSerializes(t *testing.T) {
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
		RunStaged(ctx, 5*time.Millisecond, func(context.Context) (string, error) { return fmt.Sprint(state.Load()), nil }, evaluate, nil)
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
