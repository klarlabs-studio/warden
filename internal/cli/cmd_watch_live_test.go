package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// installExecutingDocker puts a fake docker on PATH whose `run` executes the
// check on the host, inside the bind-mounted worktree, so a test can assert
// what the check saw there without a container engine.
func installExecutingDocker(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake runtime")
	}
	bin := t.TempDir()
	imageID := "sha256:" + strings.Repeat("a", 64)
	script := `#!/bin/sh
case "$1" in
info) echo '["name=seccomp,profile=builtin"]';;
image) printf '%s' '[{"Id":"` + imageID + `","Os":"linux","Architecture":"arm64"}]';;
run)
  src=""
  while [ $# -gt 1 ]; do
    case "$1" in
    --mount) src=$(printf '%s' "$2" | sed 's/.*src=\([^,]*\),dst=.*/\1/'); shift;;
    esac
    shift
  done
  script=$(printf '%s' "$1" | sed "s#/workspace#$src#g")
  cd "$src" && exec sh -c "$script";;
rm) exit 0;;
*) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// liveCheck passes only when the worktree it runs in holds the live
// checkout's unstaged edit and untracked file, and not its ignored file.
const liveCheck = `test "$(cat tracked)" = edited && test "$(cat untracked)" = new && test ! -e ignored.log`

func liveWatchRepo(t *testing.T, command string) string {
	t.Helper()
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	cfg := "execution: {image: checks:local, runtime: docker}\nsteps: {pre_commit: [test]}\ncommands: {test: '" + command + "'}\n"
	dir := repoWithConfig(t, cfg)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageFile(t, dir, "tracked", "committed")
	gitIn(t, dir, "add", ".gitignore", ".warden.yaml")
	gitIn(t, dir, "commit", "-q", "--no-verify", "-m", "files")
	for name, content := range map[string]string{"tracked": "edited", "untracked": "new", "ignored.log": "ignored"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func indexSum(t *testing.T, dir string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func looseObjectCount(t *testing.T, dir string) int {
	t.Helper()
	for _, line := range strings.Split(gitOutput(t, dir, "count-objects", "-v"), "\n") {
		if v, ok := strings.CutPrefix(line, "count: "); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("no loose object count")
	return 0
}

// addAllTreeOf is the snapshot computed without warden: the real `git add -A`
// in a copy of the worktree.
func addAllTreeOf(t *testing.T, dir string) string {
	t.Helper()
	copyDir := filepath.Join(t.TempDir(), "copy")
	if out, err := exec.Command("cp", "-R", dir, copyDir).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v %s", err, out)
	}
	gitIn(t, copyDir, "add", "-A")
	return gitOutput(t, copyDir, "write-tree")
}

func readLiveStatus(t *testing.T, dir string) domain.FeedbackDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "warden", "feedback", "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc domain.FeedbackDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Live mode under a container configuration checks the working tree as
// `git add -A` would stage it, in a disposable worktree, and records it in
// the feedback document — without writing the index, saving a prediction or
// writing a note, and without writing objects for a snapshot that did not
// change.
func TestWatchLiveContainerOnce(t *testing.T) {
	dir := liveWatchRepo(t, liveCheck)
	installExecutingDocker(t)
	indexBefore := indexSum(t, dir)
	want := addAllTreeOf(t, dir)

	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--once"}, &out, &errb); code != 0 {
		t.Fatalf("live --once: %d\n%s\n%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "live checks passed") {
		t.Fatalf("output: %s", out.String())
	}
	doc := readLiveStatus(t, dir)
	if doc.Mode != domain.FeedbackModeLive || doc.State != domain.FeedbackStopped || doc.Snapshot.Tree != want {
		t.Fatalf("document: mode %q state %q tree %s (git add -A: %s)", doc.Mode, doc.State, doc.Snapshot.Tree, want)
	}
	if doc.Snapshot.Base != gitOutput(t, dir, "rev-parse", "HEAD") || doc.EvaluatedAt == nil {
		t.Fatalf("snapshot %+v evaluated %v", doc.Snapshot, doc.EvaluatedAt)
	}
	if len(doc.Steps) != 1 || doc.Steps[0].Status != domain.FeedbackStepPass || doc.Steps[0].Prediction.Saved {
		t.Fatalf("steps: %+v", doc.Steps)
	}
	if indexSum(t, dir) != indexBefore {
		t.Fatal("live watch rewrote the index")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("index.lock left behind: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".git", "warden", "predictions")); len(entries) != 0 {
		t.Fatalf("live watch saved predictions: %v", entries)
	}
	if notes := gitOutput(t, dir, "notes", "--ref=warden", "list"); notes != "" {
		t.Fatalf("live watch wrote notes: %s", notes)
	}
	if strings.Contains(out.String(), "prediction") {
		t.Fatalf("live watch reported a prediction: %s", out.String())
	}

	// The reader recomputes the live snapshot and writes nothing doing so.
	objects := looseObjectCount(t, dir)
	doc, err := readFeedbackHere(t)
	if err != nil || doc.Freshness != domain.FreshnessCurrent {
		t.Fatalf("reader: %v %+v", err, doc)
	}
	if n := looseObjectCount(t, dir); n != objects {
		t.Fatalf("the reader wrote %d objects", n-objects)
	}
	// An unchanged snapshot evaluated again writes no objects either.
	out.Reset()
	if code := cmdWatch([]string{"--once"}, &out, &errb); code != 0 {
		t.Fatalf("repeat: %d %s", code, errb.String())
	}
	if n := looseObjectCount(t, dir); n != objects {
		t.Fatalf("re-evaluating an unchanged snapshot wrote %d objects", n-objects)
	}
	// Editing an untracked file moves the live snapshot; staging does not.
	if err := os.WriteFile(filepath.Join(dir, "untracked"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc, err := readFeedbackHere(t); err != nil || doc.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("after editing an untracked file: %v %q", err, doc.Freshness)
	}
	if code := cmdWatch([]string{"--once"}, &out, &errb); code != 1 {
		t.Fatalf("the check should fail on the changed untracked file: %d %s", code, out.String())
	}
	gitIn(t, dir, "add", "untracked")
	if doc, err := readFeedbackHere(t); err != nil || doc.Freshness != domain.FreshnessCurrent {
		t.Fatalf("staging changed the live snapshot: %v %q", err, doc.Freshness)
	}
	if indexSum(t, dir) == indexBefore {
		t.Fatal("test setup: git add did not change the index")
	}
}

func readFeedbackHere(t *testing.T) (domain.FeedbackDocument, error) {
	t.Helper()
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	return readFeedback(svc)
}

// The live loop evaluates a change, and while nothing changes its polls write
// no objects at all: the fingerprint is a walk of the files, and the snapshot
// (which stages into the object store) is computed only when it moves.
func TestWatchLiveContainerLoop(t *testing.T) {
	dir := liveWatchRepo(t, liveCheck)
	installExecutingDocker(t)
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb lockedBuffer
	done := make(chan int, 1)
	go func() { done <- runLiveContainerWatch(ctx, svc, dir, false, &out, &errb) }()
	defer func() {
		cancel()
		<-done
	}()
	waitDoc := func(what string, cond func(domain.FeedbackDocument) bool) domain.FeedbackDocument {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(filepath.Join(dir, ".git", "warden", "feedback", "status.json")); err == nil {
				var doc domain.FeedbackDocument
				if json.Unmarshal(data, &doc) == nil && cond(doc) {
					return doc
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s\n%s\n%s", what, out.String(), errb.String())
		return domain.FeedbackDocument{}
	}
	first := waitDoc("the first evaluation", func(d domain.FeedbackDocument) bool {
		return d.State == domain.FeedbackComplete && len(d.Steps) == 1 && d.Steps[0].Status == domain.FeedbackStepPass
	})

	objects := looseObjectCount(t, dir)
	time.Sleep(6 * pollInterval)
	if n := looseObjectCount(t, dir); n != objects {
		t.Fatalf("polls with nothing changed wrote %d objects", n-objects)
	}
	if doc := readLiveStatus(t, dir); doc.Snapshot.ID != first.Snapshot.ID || doc.EvaluatedAt == nil || !doc.EvaluatedAt.Equal(*first.EvaluatedAt) {
		t.Fatal("an unchanged working tree was evaluated again")
	}

	// An ignored file changing re-samples but finds the same snapshot.
	if err := os.WriteFile(filepath.Join(dir, "ignored.log"), []byte("rewritten"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * pollInterval)
	if doc := readLiveStatus(t, dir); doc.Snapshot.ID != first.Snapshot.ID || !doc.EvaluatedAt.Equal(*first.EvaluatedAt) {
		t.Fatal("an ignored file's change was evaluated as a new snapshot")
	}

	if err := os.WriteFile(filepath.Join(dir, "tracked"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := waitDoc("the edit's evaluation", func(d domain.FeedbackDocument) bool {
		return d.Snapshot.ID != first.Snapshot.ID && d.State == domain.FeedbackComplete
	})
	if len(failed.Steps) != 1 || failed.Steps[0].Status != domain.FeedbackStepFail {
		t.Fatalf("steps after a breaking edit: %+v", failed.Steps)
	}
	if failed.Snapshot.Tree != addAllTreeOf(t, dir) {
		t.Fatal("the evaluated snapshot is not the working tree")
	}
}
