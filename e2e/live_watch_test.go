package e2e

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// Container-aware live watch (design 0005, scenario 4). The right answers are
// computed without warden: the tree comes from the real `git add -A` in a
// copy of the worktree, the image ID from the engine, the index from its
// bytes on disk.

// liveSees is a check that passes only in a worktree holding the live
// checkout's unstaged edit and untracked file, and not its ignored file.
const liveSees = `test "$(cat sample)" = edited && test "$(cat untracked)" = new && test ! -e ignored.log`

// liveRepo configures a golden repo for live container checks running
// command, with sample and .gitignore committed, then leaves an unstaged edit,
// an untracked file and an ignored file in the worktree.
func liveRepo(t *testing.T, image, engine, command string) *goldenRepo {
	t.Helper()
	g := newGoldenRepo(t)
	cfg := fmt.Sprintf(`hooks: { pre_commit: true }
execution: { image: %q, runtime: %q }
steps: { pre_commit: [test], pre_push: [test] }
commands: { test: %q }
rules: []
notify: false
`, image, engine, command)
	writeText(t, filepath.Join(g.dir, ".warden.yaml"), cfg)
	writeText(t, filepath.Join(g.dir, ".gitignore"), "*.log\n")
	writeText(t, filepath.Join(g.dir, "sample"), "initial")
	g.git("add", ".warden.yaml", ".gitignore", "sample")
	g.git("commit", "--no-verify", "-m", "configure live checks")
	writeText(t, filepath.Join(g.dir, "sample"), "edited")
	writeText(t, filepath.Join(g.dir, "untracked"), "new")
	writeText(t, filepath.Join(g.dir, "ignored.log"), "ignored")
	return g
}

// addAllTree is what `git add -A` would stage, from a copy of the worktree.
func addAllTree(t *testing.T, dir string) string {
	t.Helper()
	copyDir := filepath.Join(t.TempDir(), "copy")
	if out, err := exec.Command("cp", "-R", dir, copyDir).CombinedOutput(); err != nil {
		t.Fatalf("copy worktree: %v %s", err, out)
	}
	gitAt(t, copyDir, "add", "-A")
	return gitAt(t, copyDir, "write-tree")
}

func indexDigest(t *testing.T, dir string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// watchIndexLock reports whether .git/index.lock ever appears until stop is
// called, sampling as fast as it can.
func watchIndexLock(dir string) (stop func() bool) {
	var seen atomic.Bool
	done := make(chan struct{})
	finished := make(chan struct{})
	lock := filepath.Join(dir, ".git", "index.lock")
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := os.Lstat(lock); err == nil {
				seen.Store(true)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	return func() bool {
		close(done)
		<-finished
		return seen.Load()
	}
}

// liveWatcherProcess starts `warden watch` (live mode) in dir.
func liveWatcherProcess(t *testing.T, dir string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(wardenBin, "watch")
	cmd.Dir = dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+t.TempDir())
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, out
}

// Scenario 4. Live mode checks the working tree inside the container (the
// unstaged edit and the untracked file are there, the ignored file is not),
// and leaves the repository as it found it: the index byte-identical, no
// index.lock ever taken, no note, no prediction.
func TestGoldenFleet_LiveContainerWatchChecksTheWorkingTree(t *testing.T) {
	image, engine, cli := feedbackEngine(t)
	g := liveRepo(t, image, engine, liveSees)
	before := indexDigest(t, g.dir)
	want := addAllTree(t, g.dir)

	lockSeen := watchIndexLock(g.dir)
	out, code := g.wardenEnv(nil, "watch", "--once")
	if lockSeen() {
		t.Fatal("index.lock appeared while the live watcher ran")
	}
	if code != 0 {
		t.Fatalf("watch --once: %d %s", code, out)
	}
	if indexDigest(t, g.dir) != before {
		t.Fatal("the live watcher changed .git/index")
	}
	doc, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatal("no status.json written")
	}
	if doc.Mode != domain.FeedbackModeLive || doc.Snapshot.Tree != want {
		t.Fatalf("mode %q tree %s; git add -A in a copy writes %s", doc.Mode, doc.Snapshot.Tree, want)
	}
	if head := gitAt(t, g.dir, "rev-parse", "HEAD"); doc.Snapshot.Base != head {
		t.Fatalf("snapshot.base %s, HEAD %s", doc.Snapshot.Base, head)
	}
	step, ok := testStep(doc)
	if !ok || step.Status != domain.FeedbackStepPass || step.Prediction.Saved {
		t.Fatalf("step: %+v\n%s", doc.Steps, out)
	}
	if want := engineImageID(t, engine, cli, image); step.Environment == nil || step.Environment.ImageID != want {
		t.Fatalf("environment %+v, engine says image %s", step.Environment, want)
	}
	if notes := gitAt(t, g.dir, "notes", "--ref=warden", "list"); notes != "" {
		t.Fatalf("the live watcher wrote notes: %s", notes)
	}
	if found, _ := filepath.Glob(filepath.Join(g.dir, ".git", "warden", "predictions", "*.json")); len(found) != 0 {
		t.Fatalf("the live watcher saved predictions: %v", found)
	}
	if left := wardenWorktrees(t, g.dir); len(left) != 0 {
		t.Fatalf("the live watcher left worktrees registered: %v", left)
	}
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent {
		t.Fatalf("freshness %q right after the run", got.Freshness)
	}
	// Staging the same content leaves the live snapshot where it was; editing
	// the untracked file moves it.
	g.git("add", "sample")
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent {
		t.Fatalf("freshness %q after staging the checked content", got.Freshness)
	}
	writeText(t, filepath.Join(g.dir, "untracked"), "newer")
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("freshness %q after editing an untracked file", got.Freshness)
	}
}

// The ignored file stays out of the container: a check that needs it fails.
// Without this, the passing scenario's `test ! -e ignored.log` could be
// passing for a reason that has nothing to do with the snapshot.
func TestGoldenFleet_LiveContainerWatchExcludesIgnoredFiles(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := liveRepo(t, image, engine, `test -e ignored.log`)
	out, code := g.wardenEnv(nil, "watch", "--once")
	if code != 1 {
		t.Fatalf("a check needing the ignored file passed: %d %s", code, out)
	}
	doc, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatal("no status.json written")
	}
	if step, ok := testStep(doc); !ok || step.Status != domain.FeedbackStepFail {
		t.Fatalf("step: %+v", doc.Steps)
	}
	if strings.Contains(gitAt(t, g.dir, "ls-tree", "-r", "--name-only", doc.Snapshot.Tree), "ignored.log") {
		t.Fatal("the snapshot tree holds the ignored file")
	}
}

// A commit made while a live check runs succeeds, and the watcher then
// evaluates the new snapshot: the same content on the new HEAD.
func TestGoldenFleet_LiveContainerWatchCommitDuringACheck(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := liveRepo(t, image, engine, "sleep 3 && "+liveSees)
	want := addAllTree(t, g.dir)

	watcher, out := liveWatcherProcess(t, g.dir)
	waitForStatus(t, g.dir, "the live check to be running", func(d domain.FeedbackDocument) bool {
		s, ok := testStep(d)
		return ok && s.Status == domain.FeedbackStepRunning && d.Snapshot.Tree == want
	})
	g.git("add", "sample", "untracked")
	if msg, code := gitTryAt(g.dir, "commit", "--no-verify", "-m", "commit while the live watcher checks"); code != 0 {
		t.Fatalf("commit during a live check: %d %s\nwatcher:\n%s", code, msg, out.String())
	}
	head := gitAt(t, g.dir, "rev-parse", "HEAD")
	if tree := gitAt(t, g.dir, "rev-parse", "HEAD^{tree}"); tree != want {
		t.Fatalf("test setup: committed tree %s, checked %s", tree, want)
	}
	doc := waitForStatus(t, g.dir, "the new snapshot's evaluation", func(d domain.FeedbackDocument) bool {
		return d.Snapshot.Base == head && d.State == domain.FeedbackComplete
	})
	if step, ok := testStep(doc); doc.Snapshot.Tree != want || !ok || step.Status != domain.FeedbackStepPass {
		t.Fatalf("after the commit: tree %s steps %+v\n%s", doc.Snapshot.Tree, doc.Steps, out.String())
	}
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent {
		t.Fatalf("freshness %q after the commit's evaluation", got.Freshness)
	}
	_ = watcher.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- watcher.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the live watcher did not stop on interrupt:\n%s", out.String())
	}
	if left := wardenWorktrees(t, g.dir); len(left) != 0 {
		t.Fatalf("worktrees left registered: %v", left)
	}
}
