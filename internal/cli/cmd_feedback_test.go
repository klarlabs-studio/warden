package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/attach"
	"go.klarlabs.de/warden/internal/infrastructure/feedback"
)

func feedbackStatus(t *testing.T) (domain.FeedbackDocument, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdFeedback([]string{"status", "--format", "json"}, &out, &errb)
	var doc domain.FeedbackDocument
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("output is not a document: %v\n%s", err, out.String())
		}
	}
	return doc, errb.String(), code
}

func statusPath(t *testing.T) string {
	t.Helper()
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	return feedback.StatusPath(gitDir)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestFeedbackStatusInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"show"}, {"status", "--format", "xml"}, {"status", "extra"}, {"status", "--nope"}} {
		var out, errb bytes.Buffer
		if code := cmdFeedback(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"warden", "feedback"}, &out, &errb); code != 2 {
		t.Fatalf("dispatch: %d", code)
	}
}

func TestFeedbackStatusWithoutADocument(t *testing.T) {
	repoWithConfig(t, "commands: { test: 'true' }\n")
	if _, errb, code := feedbackStatus(t); code != 1 || !strings.Contains(errb, "warden watch --staged") {
		t.Fatalf("%d %s", code, errb)
	}
	if err := os.MkdirAll(filepath.Dir(statusPath(t)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath(t), []byte(`{"schema":"warden.feedback/v0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errb, code := feedbackStatus(t); code != 1 || !strings.Contains(errb, "invalid feedback document") {
		t.Fatalf("%d %s", code, errb)
	}
}

// The document must describe what the watcher checked, as git and the engine
// would state it independently, and the reader must notice the moment that
// stops being true, with no watcher running.
func TestFeedbackStatusFollowsTheRepository(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 {
		t.Fatalf("once: %d %s %s", code, out.String(), errb.String())
	}
	doc, errText, code := feedbackStatus(t)
	if code != 0 {
		t.Fatalf("status: %d %s", code, errText)
	}
	if doc.Freshness != domain.FreshnessCurrent || doc.State != domain.FeedbackStopped || doc.Mode != domain.FeedbackModeStaged {
		t.Fatalf("after --once: %+v", doc)
	}
	if want := gitOut(t, dir, "write-tree"); doc.Snapshot.Tree != want {
		t.Fatalf("snapshot tree %s, git write-tree %s", doc.Snapshot.Tree, want)
	}
	if doc.Snapshot.Base != gitOut(t, dir, "rev-parse", "HEAD") || doc.Snapshot.EnvironmentDigest == "" || doc.EvaluatedAt == nil {
		t.Fatalf("snapshot: %+v", doc.Snapshot)
	}
	if len(doc.Steps) != 1 {
		t.Fatalf("steps: %+v", doc.Steps)
	}
	step := doc.Steps[0]
	if step.Step != "test" || step.Status != domain.FeedbackStepPass || !step.Prediction.Saved || step.Prediction.ExpiresAt == nil ||
		step.Environment == nil || step.Environment.ImageID != "sha256:"+strings.Repeat("a", 64) || step.StartedAt == nil {
		t.Fatalf("step: %+v", step)
	}
	if real, _ := filepath.EvalSymlinks(dir); doc.Worktree != real && doc.Worktree != dir {
		t.Fatalf("worktree %q, want %q", doc.Worktree, dir)
	}

	// A second evaluation serves the step from the prediction just saved.
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 {
		t.Fatalf("repeat: %d %s", code, errb.String())
	}
	if doc, _, _ = feedbackStatus(t); doc.Steps[0].Status != domain.FeedbackStepCached || doc.Steps[0].Prediction.ExpiresAt == nil {
		t.Fatalf("repeat: %+v", doc.Steps)
	}

	stageFile(t, dir, "sample", "restaged\n")
	if doc, _, _ = feedbackStatus(t); doc.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("after git add: %q", doc.Freshness)
	}
	// So does a configuration change, which alters what a commit would run.
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(stagedWatchConfig+"notify: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc, _, _ = feedbackStatus(t); doc.Freshness != domain.FreshnessStaleSnapshot {
		t.Fatalf("after a config change: %q", doc.Freshness)
	}
}

// A watcher that died (SIGKILL, crash) leaves "running" behind. The reader
// must say the producer is gone, not that checks are running.
func TestFeedbackStatusProducerGone(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	var out, errb bytes.Buffer
	if code := cmdWatch([]string{"--staged", "--once"}, &out, &errb); code != 0 {
		t.Fatalf("once: %d %s", code, errb.String())
	}
	doc, err := feedback.Read(statusPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	doc.State = domain.FeedbackRunning
	doc.Producer.PID = dead.Process.Pid
	if err := feedback.Write(statusPath(t), doc); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := feedbackStatus(t); got.Freshness != domain.FreshnessProducerGone {
		t.Fatalf("dead producer: %q", got.Freshness)
	}
	// A live pid whose heartbeat stopped is gone too (pids are reused).
	doc.Producer.PID = os.Getpid()
	doc.Producer.HeartbeatAt = time.Now().Add(-time.Hour)
	if err := feedback.Write(statusPath(t), doc); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := feedbackStatus(t); got.Freshness != domain.FreshnessProducerGone {
		t.Fatalf("stalled producer: %q", got.Freshness)
	}
	// A producer may not assert freshness for the reader.
	doc.Freshness = domain.FreshnessCurrent
	if err := feedback.Write(statusPath(t), doc); err != nil {
		t.Fatal(err)
	}
	if _, errb, code := feedbackStatus(t); code != 1 || !strings.Contains(errb, "may not assert freshness") {
		t.Fatalf("asserted freshness: %d %s", code, errb)
	}
}

// One worktree, one writer: a second watcher exits 75 naming the holder.
func TestWatchStagedSecondWatcherExitsContention(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	holder, err := feedback.Acquire(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Release() }()
	var out, errb bytes.Buffer
	code := cmdWatch([]string{"--staged", "--once"}, &out, &errb)
	if code != exitContention || !strings.Contains(errb.String(), "another watcher (pid ") {
		t.Fatalf("second watcher: %d %s", code, errb.String())
	}
	if _, err := os.Stat(feedback.StatusPath(gitDir)); !os.IsNotExist(err) {
		t.Fatal("the second watcher wrote the document")
	}
}

// Only the watcher writes the document. A gate run, which reuses the same
// staged predictions, must not.
func TestGateRunDoesNotWriteFeedback(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	var out, errb bytes.Buffer
	if code := cmdRun([]string{"pre-commit"}, &out, &errb); code != 0 {
		t.Fatalf("gate: %d %s %s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Dir(statusPath(t))); !os.IsNotExist(err) {
		t.Fatal("a gate run created the feedback directory")
	}
}

// A watcher whose lock was taken over (its heartbeat stalled) stops with
// exitContention and leaves the new holder's document alone.
func TestWatchStagedStopsWhenItsLockIsTakenOver(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	interval := feedbackHeartbeatInterval
	feedbackHeartbeatInterval = 50 * time.Millisecond
	t.Cleanup(func() { feedbackHeartbeatInterval = interval })
	dir := repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the first evaluation", func() bool {
		return strings.Contains(stdout.String(), "staged checks passed")
	})
	path := statusPath(t)
	lockPath := filepath.Join(filepath.Dir(path), "watch.lock")
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()
	other, _ := json.Marshal(feedback.Holder{PID: sleeper.Process.Pid, StartedAt: time.Now()})
	if err := os.WriteFile(lockPath, other, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the watcher to notice", func() bool {
		return strings.Contains(stderr.String(), "taken over")
	})
	before, _ := os.ReadFile(path)
	if code := stop(); code != exitContention {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("the displaced watcher rewrote the document")
	}
	if data, _ := os.ReadFile(lockPath); !bytes.Equal(data, other) {
		t.Fatal("the displaced watcher removed the new holder's lock")
	}
}

// The running watcher heartbeats, and records a stopped state on exit.
func TestWatchStagedHeartbeatsAndStops(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	interval := feedbackHeartbeatInterval
	feedbackHeartbeatInterval = 50 * time.Millisecond
	t.Cleanup(func() { feedbackHeartbeatInterval = interval })
	dir := repoWithConfig(t, stagedWatchConfig)
	installFakeDocker(t, fakeDocker{})
	stageFile(t, dir, "sample", "staged\n")
	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the first evaluation", func() bool {
		return strings.Contains(stdout.String(), "staged checks passed")
	})
	waitFor(t, 20*time.Second, "a complete, current document", func() bool {
		doc, _, code := feedbackStatus(t)
		return code == 0 && doc.State == domain.FeedbackComplete && doc.Freshness == domain.FreshnessCurrent
	})
	first, _ := feedback.Read(statusPath(t))
	waitFor(t, 20*time.Second, "a heartbeat", func() bool {
		doc, err := feedback.Read(statusPath(t))
		return err == nil && doc.Producer.HeartbeatAt.After(first.Producer.HeartbeatAt)
	})
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	doc, _, _ := feedbackStatus(t)
	if doc.State != domain.FeedbackStopped || doc.Freshness != domain.FreshnessCurrent || doc.Steps[0].Status != domain.FeedbackStepPass {
		t.Fatalf("after stop: %+v", doc)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(statusPath(t)), "watch.lock")); !os.IsNotExist(err) {
		t.Fatal("the lock outlived the watcher")
	}
}

// Yielding to a gate run cancels the evaluation in flight. The document must
// not keep showing a step as running that will never finish.
func TestWatchStagedFeedbackWhileYielding(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	started := filepath.Join(t.TempDir(), "started")
	installFakeDocker(t, fakeDocker{started: started, runSeconds: 5})
	stageFile(t, dir, "sample", "staged\n")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := svc.GitDir()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, stop := startStagedWatch(t, dir)
	waitFor(t, 20*time.Second, "the check to start", func() bool {
		doc, err := feedback.Read(statusPath(t))
		return err == nil && len(doc.Steps) == 1 && doc.Steps[0].Status == domain.FeedbackStepRunning
	})
	gate := attach.NewServer(gitDir)
	if gate == nil {
		t.Fatal("could not publish a gate socket")
	}
	defer gate.Close()
	waitFor(t, 20*time.Second, "the watcher to yield", func() bool {
		return strings.Contains(stdout.String(), "a gate run is in progress")
	})
	doc, err := feedback.Read(statusPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if doc.State != domain.FeedbackPending || !strings.Contains(doc.Message, "gate run") || doc.Steps[0].Status != domain.FeedbackStepPending {
		t.Fatalf("while yielding: %+v", doc)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}

// When the snapshot cannot be computed the evaluation still runs, but nothing
// is recorded against a snapshot the watcher did not establish, and the
// reason is reported once rather than on every evaluation.
func TestFeedbackSessionBeginFailure(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, stagedWatchConfig)
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fb, code := startFeedback(ctx, svc, dir, &errb)
	if fb == nil {
		t.Fatalf("start: %d %s", code, errb.String())
	}
	defer fb.close()
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte("steps: [not, a, map]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := fb.begin(ctx, svc, dir); err != nil {
			t.Fatal(err)
		}
		fb.record(ctx, nil)
	}
	if n := strings.Count(errb.String(), "background feedback not recorded"); n != 1 {
		t.Fatalf("reported %d times: %s", n, errb.String())
	}
	if _, err := os.Stat(statusPath(t)); !os.IsNotExist(err) {
		t.Fatal("a document was written without a snapshot")
	}
	cancel()
	if err := fb.begin(ctx, svc, dir); err == nil {
		t.Fatal("a cancelled begin reported success")
	}
	if environmentDigest("") != "" || environmentDigest("x") == "" {
		t.Fatal("environment digest")
	}
}
