package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// Background feedback (warden.feedback/v1) golden-fleet scenarios. Each one
// computes the right answer without warden — git write-tree, the engine's own
// image inspection, git's worktree list — and compares the artifact warden
// wrote, not just an exit status. Gated like the other container scenarios.

// feedbackEngine returns the configured runtime and its CLI, or skips.
func feedbackEngine(t *testing.T) (image, engine, cli string) {
	t.Helper()
	image = os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	engine = os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if engine == "" {
		engine = "docker"
	}
	cli = engine
	if engine == "apple" {
		cli = "container"
	}
	return image, engine, cli
}

// feedbackRepo configures a golden repo for staged checks running command,
// with sample committed as "initial".
func feedbackRepo(t *testing.T, image, engine, command string) *goldenRepo {
	t.Helper()
	g := newGoldenRepo(t)
	cfg := fmt.Sprintf(`hooks: { pre_commit: true }
execution: { image: %q, runtime: %q }
predictive_cache: [test]
steps: { pre_commit: [test], pre_push: [test] }
commands: { test: %q }
rules: []
notify: false
`, image, engine, command)
	writeText(t, filepath.Join(g.dir, ".warden.yaml"), cfg)
	writeText(t, filepath.Join(g.dir, "sample"), "initial")
	g.git("add", ".warden.yaml", "sample")
	g.git("commit", "--no-verify", "-m", "configure staged checks")
	return g
}

func writeText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// statusFile is the document's path for the worktree at dir.
func statusFile(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(gitAt(t, dir, "rev-parse", "--absolute-git-dir"), "warden", "feedback", "status.json")
}

// readStatus parses the document straight from disk, as written.
func readStatus(t *testing.T, dir string) (domain.FeedbackDocument, bool) {
	t.Helper()
	data, err := os.ReadFile(statusFile(t, dir))
	if err != nil {
		return domain.FeedbackDocument{}, false
	}
	var doc domain.FeedbackDocument
	if json.Unmarshal(data, &doc) != nil {
		return domain.FeedbackDocument{}, false
	}
	return doc, true
}

// feedbackStatus runs `warden feedback status` in dir.
func feedbackStatus(t *testing.T, dir string) domain.FeedbackDocument {
	t.Helper()
	cmd := exec.Command(wardenBin, "feedback", "status", "--format", "json")
	cmd.Dir = dir
	cmd.Env = hookEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("feedback status in %s: %v\n%s", dir, err, out)
	}
	var doc domain.FeedbackDocument
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("feedback status output: %v\n%s", err, out)
	}
	return doc
}

// waitForStatus polls the document on disk until cond holds.
func waitForStatus(t *testing.T, dir, what string, cond func(domain.FeedbackDocument) bool) domain.FeedbackDocument {
	t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	for {
		if doc, ok := readStatus(t, dir); ok && cond(doc) {
			return doc
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(statusFile(t, dir))
			t.Fatalf("timed out waiting for %s; last document:\n%s", what, data)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// watcherProcess starts `warden watch --staged` in dir and returns the
// process, so a scenario can kill it outright.
func watcherProcess(t *testing.T, dir, keyDir string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(wardenBin, "watch", "--staged")
	cmd.Dir = dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+keyDir)
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

func testStep(doc domain.FeedbackDocument) (domain.FeedbackStep, bool) {
	for _, s := range doc.Steps {
		if s.Step == "test" {
			return s, true
		}
	}
	return domain.FeedbackStep{}, false
}

// Scenario 1. The document must describe exactly what was checked: the staged
// tree (not the live file), under the image the engine says it ran. Once the
// index moves, the reader must say so with no watcher running.
func TestGoldenFleet_FeedbackStatusMatchesReality(t *testing.T) {
	image, engine, cli := feedbackEngine(t)
	g := feedbackRepo(t, image, engine, `test "$(cat sample)" = good`)
	keyDir := t.TempDir()
	writeText(t, filepath.Join(g.dir, "sample"), "good")
	g.git("add", "sample")
	writeText(t, filepath.Join(g.dir, "sample"), "bad") // the live file disagrees
	if out, code := g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, "watch", "--staged", "--once"); code != 0 {
		t.Fatalf("watch --staged --once: %d %s", code, out)
	}
	doc, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatal("no status.json written")
	}
	if want := gitAt(t, g.dir, "write-tree"); doc.Snapshot.Tree != want {
		t.Fatalf("snapshot.tree %s, git write-tree %s", doc.Snapshot.Tree, want)
	}
	if want := gitAt(t, g.dir, "rev-parse", "HEAD"); doc.Snapshot.Base != want {
		t.Fatalf("snapshot.base %s, HEAD %s", doc.Snapshot.Base, want)
	}
	step, ok := testStep(doc)
	if !ok || step.Status != domain.FeedbackStepPass || !step.Prediction.Saved {
		t.Fatalf("step: %+v", doc.Steps)
	}
	if want := engineImageID(t, engine, cli, image); step.Environment == nil || step.Environment.ImageID != want {
		t.Fatalf("environment %+v, engine says image %s", step.Environment, want)
	}
	// The prediction the document announces is the one on disk.
	var prediction domain.Prediction
	if err := json.Unmarshal(onlyPrediction(t, filepath.Join(g.dir, ".git", "warden", "predictions")), &prediction); err != nil {
		t.Fatal(err)
	}
	if prediction.Contract.TreeSHA != doc.Snapshot.Tree || !step.Prediction.ExpiresAt.Equal(prediction.CreatedAt.Add(domain.PredictionTTL)) {
		t.Fatalf("document and prediction disagree: tree %s/%s expiry %v/%v",
			prediction.Contract.TreeSHA, doc.Snapshot.Tree, step.Prediction.ExpiresAt, prediction.CreatedAt.Add(domain.PredictionTTL))
	}
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent {
		t.Fatalf("freshness %q right after the run", got.Freshness)
	}

	writeText(t, filepath.Join(g.dir, "sample"), "good again")
	g.git("add", "sample")
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("freshness %q after git add with no watcher running", got.Freshness)
	}
}

// Scenario 2. status.json is unsigned and advisory. A forged document saying
// the staged contents pass must change nothing at the gate.
func TestGoldenFleet_GateIgnoresForgedFeedback(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := feedbackRepo(t, image, engine, `test "$(cat sample)" = good`)
	keyDir := t.TempDir()
	warden := func(args ...string) (string, int) {
		return g.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + keyDir}, args...)
	}
	writeText(t, filepath.Join(g.dir, "sample"), "good")
	g.git("add", "sample")
	if out, code := warden("watch", "--staged", "--once"); code != 0 {
		t.Fatalf("watch: %d %s", code, out)
	}
	real, ok := readStatus(t, g.dir)
	if !ok {
		t.Fatal("no status.json written")
	}
	// Stage failing contents and forge a passing, internally consistent
	// document for exactly that snapshot.
	writeText(t, filepath.Join(g.dir, "sample"), "bad")
	g.git("add", "sample")
	forged := real
	forged.State = domain.FeedbackComplete
	forged.Snapshot = domain.NewFeedbackSnapshot(gitAt(t, g.dir, "write-tree"), real.Snapshot.Base, real.Snapshot.Branch,
		real.Snapshot.ConfigurationDigest, real.Snapshot.EnvironmentDigest)
	forged.Producer.PID = os.Getpid()
	forged.Producer.HeartbeatAt = time.Now().UTC()
	data, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, statusFile(t, g.dir), string(data))
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent || got.Steps[0].Status != domain.FeedbackStepPass {
		t.Fatalf("the forgery should read as a current pass, or this test proves nothing: %+v", got)
	}

	out, code := warden("run", "pre-commit")
	if code == 0 || strings.Contains(out, "matched signed staged prediction") {
		t.Fatalf("the gate accepted failing staged contents beside a forged pass: %d %s", code, out)
	}
	note := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
	note.Dir = g.dir
	if err := note.Run(); err == nil {
		t.Fatal("a note exists after a failing gate run")
	}
	// The gate did not rewrite the document either: only the watcher writes it.
	if after, _ := os.ReadFile(statusFile(t, g.dir)); string(after) != string(data) {
		t.Fatal("the gate run wrote the feedback document")
	}
}

// Scenario 5. A commit made while a background check runs must succeed; the
// document then moves to the new snapshot and settles on idle (the index
// matches the new HEAD).
func TestGoldenFleet_FeedbackFollowsACommitDuringACheck(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := feedbackRepo(t, image, engine, `sleep 3 && test "$(cat sample)" = good`)
	keyDir := t.TempDir()
	writeText(t, filepath.Join(g.dir, "sample"), "good")
	g.git("add", "sample")
	stagedTree := gitAt(t, g.dir, "write-tree")

	_, out := watcherProcess(t, g.dir, keyDir)
	waitForStatus(t, g.dir, "the check to be running", func(d domain.FeedbackDocument) bool {
		s, ok := testStep(d)
		return ok && s.Status == domain.FeedbackStepRunning && d.Snapshot.Tree == stagedTree
	})
	if msg, code := gitTryAt(g.dir, "commit", "-m", "commit while the watcher checks"); code != 0 {
		t.Fatalf("commit during a background check: %d %s\nwatcher:\n%s", code, msg, out.String())
	}
	head := gitAt(t, g.dir, "rev-parse", "HEAD")
	doc := waitForStatus(t, g.dir, "idle on the new snapshot", func(d domain.FeedbackDocument) bool {
		return d.Snapshot.Base == head && d.State == domain.FeedbackIdle
	})
	if len(doc.Steps) != 0 || doc.Snapshot.Tree != gitAt(t, g.dir, "write-tree") {
		t.Fatalf("idle document: %+v", doc)
	}
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent {
		t.Fatalf("freshness %q while the watcher is idle", got.Freshness)
	}
}

func gitTryAt(dir string, args ...string) (string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		return err.Error(), -1
	}
	return string(out), 0
}

// Scenario 6. A watcher killed mid-run leaves "running" in the document. The
// reader must report producer_gone; a new watcher must take the lock over;
// and the next run must leave no warden worktree registered once the killed
// run's directory is gone.
//
// The killed run's own worktree directory survives a SIGKILL (its deferred
// teardown never runs), and the existing sweep deliberately keeps any warden
// worktree whose directory is still on disk, because it cannot tell a dead
// run's from a concurrent live one. So that one registration lasts until the
// OS reaps the temp directory. The test asserts exactly that boundary: the
// restarted watcher leaves nothing of its own, and the leftover is pruned on
// the first run after its directory disappears.
func TestGoldenFleet_FeedbackAfterAWatcherCrash(t *testing.T) {
	image, engine, cli := feedbackEngine(t)
	g := feedbackRepo(t, image, engine, `sleep 5 && test "$(cat sample)" = good`)
	keyDir := t.TempDir()
	writeText(t, filepath.Join(g.dir, "sample"), "good")
	g.git("add", "sample")

	first, _ := watcherProcess(t, g.dir, keyDir)
	waitForStatus(t, g.dir, "the check to be running", func(d domain.FeedbackDocument) bool {
		s, ok := testStep(d)
		return ok && s.Status == domain.FeedbackStepRunning
	})
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessProducerGone || got.State != domain.FeedbackRunning {
		t.Fatalf("after SIGKILL: state %q freshness %q", got.State, got.Freshness)
	}
	// The killed run's container finishes its `sleep` on its own. Let it, so
	// the restarted watcher's container does not compete with it: under that
	// contention Apple's cleanup can time out and fail the step, which is a
	// runtime matter, not what this scenario is about.
	waitForNoWardenContainers(t, engine, cli)
	orphans := wardenWorktrees(t, g.dir)
	if len(orphans) != 1 {
		t.Fatalf("the killed run should leave exactly its own worktree: %v", orphans)
	}

	second, out := watcherProcess(t, g.dir, keyDir)
	doc := waitForStatus(t, g.dir, "the restarted watcher's evaluation", func(d domain.FeedbackDocument) bool {
		s, ok := testStep(d)
		return d.Producer.PID == second.Process.Pid && d.State == domain.FeedbackComplete && ok && s.Status == domain.FeedbackStepPass
	})
	if strings.Contains(out.String(), "another watcher") {
		t.Fatalf("the restarted watcher did not take the stale lock over:\n%s", out.String())
	}
	if got := feedbackStatus(t, g.dir); got.Freshness != domain.FreshnessCurrent || got.Producer.PID != doc.Producer.PID {
		t.Fatalf("after restart: %+v", got)
	}
	if left := wardenWorktrees(t, g.dir); len(left) != 1 || left[0] != orphans[0] {
		t.Fatalf("the restarted watcher left worktrees of its own: %v (killed run's: %v)", left, orphans)
	}

	// The OS reaps the killed run's temp directory; the next run prunes it.
	if err := os.RemoveAll(orphans[0]); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(g.dir, "sample"), "good")
	writeText(t, filepath.Join(g.dir, "other"), "next")
	g.git("add", "other")
	next := gitAt(t, g.dir, "write-tree")
	waitForStatus(t, g.dir, "the next evaluation", func(d domain.FeedbackDocument) bool {
		return d.Snapshot.Tree == next && d.State == domain.FeedbackComplete
	})
	if left := wardenWorktrees(t, g.dir); len(left) != 0 {
		t.Fatalf("stale warden worktrees remain registered: %v", left)
	}
	_ = second.Process.Signal(os.Interrupt)
	_ = second.Wait()
}

// waitForNoWardenContainers waits until the engine lists no running warden
// container.
func waitForNoWardenContainers(t *testing.T, engine, cli string) {
	t.Helper()
	list := []string{"ps"}
	if engine == "apple" {
		list = []string{"list"}
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command(cli, list...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", cli, list, err, out)
		}
		if !strings.Contains(string(out), "warden-") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("the killed run's container is still running")
}

// wardenWorktrees lists the registered worktrees warden created.
func wardenWorktrees(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(gitAt(t, dir, "worktree", "list", "--porcelain"), "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok && strings.HasPrefix(filepath.Base(path), "warden-wt-") {
			out = append(out, path)
		}
	}
	return out
}

// Scenario 7. Linked worktrees each get their own document and predictions,
// from their own staged trees, with two watchers running at once.
func TestGoldenFleet_FeedbackPerWorktree(t *testing.T) {
	image, engine, _ := feedbackEngine(t)
	g := feedbackRepo(t, image, engine, `test -s sample`)
	keyDir := t.TempDir()
	dirs := map[string]string{}
	for _, name := range []string{"left", "right"} {
		dir := filepath.Join(t.TempDir(), name)
		g.git("worktree", "add", "-b", name, dir)
		writeText(t, filepath.Join(dir, "sample"), "staged in "+name)
		gitAt(t, dir, "add", "sample")
		dirs[name] = dir
	}
	trees := map[string]string{}
	for name, dir := range dirs {
		trees[name] = gitAt(t, dir, "write-tree")
		watcherProcess(t, dir, keyDir)
	}
	if trees["left"] == trees["right"] {
		t.Fatal("the worktrees must stage different trees")
	}
	for name, dir := range dirs {
		doc := waitForStatus(t, dir, name+"'s evaluation", func(d domain.FeedbackDocument) bool {
			s, ok := testStep(d)
			return d.State == domain.FeedbackComplete && ok && s.Status == domain.FeedbackStepPass
		})
		if doc.Snapshot.Tree != trees[name] || doc.Snapshot.Branch != name {
			t.Fatalf("%s: snapshot %+v, want tree %s", name, doc.Snapshot, trees[name])
		}
		if real, _ := filepath.EvalSymlinks(dir); doc.Worktree != real && doc.Worktree != dir {
			t.Fatalf("%s: worktree %q", name, doc.Worktree)
		}
		if got := feedbackStatus(t, dir); got.Freshness != domain.FreshnessCurrent || got.Snapshot.ID != doc.Snapshot.ID {
			t.Fatalf("%s: reader disagrees: %+v", name, got)
		}
		gitDir := gitAt(t, dir, "rev-parse", "--absolute-git-dir")
		var prediction domain.Prediction
		if err := json.Unmarshal(onlyPrediction(t, filepath.Join(gitDir, "warden", "predictions")), &prediction); err != nil {
			t.Fatal(err)
		}
		if prediction.Contract.TreeSHA != trees[name] {
			t.Fatalf("%s: prediction for tree %s, staged %s", name, prediction.Contract.TreeSHA, trees[name])
		}
	}
	// The main worktree has no watcher and no document of its own.
	if _, ok := readStatus(t, g.dir); ok {
		t.Fatal("a linked worktree's watcher wrote the main worktree's document")
	}
}
