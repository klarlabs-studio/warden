package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "warden-test")
	gitRun(t, dir, "config", "user.name", "warden-test")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	gitRun(t, dir, "branch", "-M", "main")
	return dir
}

var testConfig = "sha256:" + strings.Repeat("c", 64)

func validDocument(t *testing.T) domain.FeedbackDocument {
	t.Helper()
	now := time.Now().UTC()
	return domain.FeedbackDocument{
		Schema:   domain.FeedbackSchema,
		Producer: domain.FeedbackProducer{WardenVersion: "test", PID: os.Getpid(), StartedAt: now, HeartbeatAt: now},
		Mode:     domain.FeedbackModeStaged,
		State:    domain.FeedbackComplete,
		Snapshot: domain.NewFeedbackSnapshot(strings.Repeat("a", 40), strings.Repeat("b", 40), "main", testConfig, ""),
		Steps:    []domain.FeedbackStep{},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := StatusPath(filepath.Join(t.TempDir(), "git"))
	if _, err := Read(path); !errors.Is(err, ErrNoDocument) {
		t.Fatalf("missing document: %v", err)
	}
	doc := validDocument(t)
	if err := Write(path, doc); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, doc); err != nil { // replaces an existing file
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || got.Snapshot.ID != doc.Snapshot.ID {
		t.Fatalf("read back: %+v %v", got, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "status-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestReadRejectsInvalidDocuments(t *testing.T) {
	path := StatusPath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	forged := validDocument(t)
	forged.Snapshot.Tree = strings.Repeat("f", 40) // id no longer matches
	forgedData, _ := json.Marshal(forged)
	for name, data := range map[string][]byte{
		"not json":  []byte("{"),
		"forged id": forgedData,
		"oversized": append([]byte(`{"schema":"`), make([]byte, maxDocumentBytes)...),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); !errors.Is(err, domain.ErrInvalidFeedback) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Error("a directory read as a document")
	}
}

// The snapshot must match what git itself says the index would write, and
// computing it must never touch the worktree's index or take its lock: a
// reader runs whenever an editor asks, including mid-commit.
func TestCurrentSnapshotMatchesGitAndLeavesTheIndexAlone(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "a")
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	// A commit in progress holds index.lock. The snapshot must still compute.
	lock := index + ".lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := CurrentSnapshot(context.Background(), dir, testConfig, "sha256:env")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil || string(after) != string(before) {
		t.Fatal("computing the snapshot rewrote the index")
	}
	if want := gitRun(t, dir, "write-tree"); snap.Tree != want {
		t.Fatalf("tree %s, git write-tree %s", snap.Tree, want)
	}
	if snap.Base != gitRun(t, dir, "rev-parse", "HEAD") || snap.Branch != "main" || snap.EnvironmentDigest != "sha256:env" {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.ID != domain.FeedbackSnapshotID(snap.Tree, snap.Base, snap.Branch, testConfig) {
		t.Fatal("id does not match its parts")
	}
	top, err := Worktree(context.Background(), filepath.Join(dir, ".git", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if real, _ := filepath.EvalSymlinks(dir); top != real && top != dir {
		t.Fatalf("worktree %s, want %s", top, dir)
	}
}

func TestCurrentSnapshotOnAnUnbornBranch(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "trunk")
	snap, err := CurrentSnapshot(context.Background(), dir, testConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Base != "" || snap.Branch == "" || !domain.ValidTreeSHA(snap.Tree) {
		t.Fatalf("snapshot %+v", snap)
	}
	if _, err := CurrentSnapshot(context.Background(), t.TempDir(), testConfig, ""); err == nil {
		t.Fatal("a directory outside any repository produced a snapshot")
	}
}

func setStale(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockStaleAfter
	lockStaleAfter = d
	t.Cleanup(func() { lockStaleAfter = old })
}

func writeLockFile(t *testing.T, gitDir string, h Holder, age time.Duration) {
	t.Helper()
	path := LockPath(gitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(h)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	name, args := "sh", []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "exit 0"}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// livePID starts a process that outlives the test's lock checks.
func livePID(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sleep")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestLockExcludesASecondWatcher(t *testing.T) {
	gitDir := t.TempDir()
	first, err := Acquire(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Holder().PID != os.Getpid() {
		t.Fatalf("holder %+v", first.Holder())
	}
	_, err = Acquire(gitDir)
	var held *HeldError
	if !errors.As(err, &held) || held.Holder.PID != os.Getpid() || !strings.Contains(err.Error(), "pid") {
		t.Fatalf("second acquire: %v", err)
	}
	if err := first.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil { // already gone
		t.Fatal(err)
	}
	again, err := Acquire(gitDir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	_ = again.Release()
}

func TestLockHeldByALiveHeartbeatingWatcher(t *testing.T) {
	gitDir := t.TempDir()
	pid := livePID(t)
	writeLockFile(t, gitDir, Holder{PID: pid, StartedAt: time.Now()}, 0)
	_, err := Acquire(gitDir)
	var held *HeldError
	if !errors.As(err, &held) || held.Holder.PID != pid {
		t.Fatalf("acquire over a live holder: %v", err)
	}
}

func TestLockTakesOverAbandonedLocks(t *testing.T) {
	cases := map[string]func(t *testing.T, gitDir string){
		"holder exited": func(t *testing.T, gitDir string) {
			writeLockFile(t, gitDir, Holder{PID: deadPID(t), StartedAt: time.Now()}, 0)
		},
		"heartbeat stopped (pid reused)": func(t *testing.T, gitDir string) {
			setStale(t, time.Second)
			writeLockFile(t, gitDir, Holder{PID: livePID(t), StartedAt: time.Now()}, time.Minute)
		},
		"never written": func(t *testing.T, gitDir string) {
			writeLockFile(t, gitDir, Holder{}, time.Minute)
			if err := os.WriteFile(LockPath(gitDir), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Minute)
			_ = os.Chtimes(LockPath(gitDir), old, old)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			gitDir := t.TempDir()
			setup(t, gitDir)
			l, err := Acquire(gitDir)
			if err != nil {
				t.Fatalf("takeover: %v", err)
			}
			data, _ := os.ReadFile(LockPath(gitDir))
			var h Holder
			if json.Unmarshal(data, &h) != nil || h.PID != os.Getpid() {
				t.Fatalf("lock after takeover: %s", data)
			}
			if aside, _ := filepath.Glob(LockPath(gitDir) + ".stale.*"); len(aside) != 0 {
				t.Fatalf("stale lock left aside: %v", aside)
			}
			_ = l.Release()
		})
	}
}

// A lock file just created by a starting watcher may not hold its JSON yet;
// it must not be taken over.
func TestLockLeavesAFreshUnwrittenLockAlone(t *testing.T) {
	gitDir := t.TempDir()
	writeLockFile(t, gitDir, Holder{}, 0)
	if err := os.WriteFile(LockPath(gitDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var held *HeldError
	if _, err := Acquire(gitDir); !errors.As(err, &held) {
		t.Fatalf("fresh empty lock: %v", err)
	}
}

// A watcher whose lock was taken over (its heartbeat stalled, e.g. a
// suspended laptop) must learn it, and must not remove the new holder's lock.
func TestLockLostAfterTakeover(t *testing.T) {
	gitDir := t.TempDir()
	l, err := Acquire(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	writeLockFile(t, gitDir, Holder{PID: livePID(t), StartedAt: time.Now()}, 0)
	if err := l.Heartbeat(); !errors.Is(err, ErrLockLost) {
		t.Fatalf("heartbeat after takeover: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LockPath(gitDir)); err != nil {
		t.Fatal("release removed the new holder's lock")
	}
}

func TestTakeOverRestoresALockReplacedMeanwhile(t *testing.T) {
	gitDir := t.TempDir()
	path := LockPath(gitDir)
	writeLockFile(t, gitDir, Holder{PID: 7, StartedAt: time.Now()}, 0)
	judged := lockState{data: []byte(`{"pid":6}`)}
	if err := takeOver(path, judged); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"pid":7`) {
		t.Fatalf("a fresh lock was not put back: %s %v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := takeOver(path, judged); err != nil { // already gone
		t.Fatal(err)
	}
}

type recorderFixture struct {
	rec  *Recorder
	path string
	snap domain.FeedbackSnapshot
}

func newRecorderFixture(t *testing.T) recorderFixture {
	t.Helper()
	path := StatusPath(t.TempDir())
	rec := NewRecorder(RecorderOptions{Path: path, WardenVersion: "test", Mode: domain.FeedbackModeStaged, Worktree: "/w", StartedAt: time.Now(), PID: os.Getpid()})
	return recorderFixture{rec: rec, path: path, snap: domain.NewFeedbackSnapshot(strings.Repeat("a", 40), strings.Repeat("b", 40), "main", testConfig, "sha256:env1")}
}

func (f recorderFixture) read(t *testing.T) domain.FeedbackDocument {
	t.Helper()
	doc, err := Read(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRecorderWritesNothingBeforeASnapshot(t *testing.T) {
	f := newRecorderFixture(t)
	f.rec.Paused("waiting")
	if err := f.rec.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatal("a document without a snapshot was written")
	}
}

func TestRecorderFollowsAnEvaluation(t *testing.T) {
	f := newRecorderFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.rec.Begin(ctx, f.snap, []domain.StepName{"lint", "test", "extra"})
	doc := f.read(t)
	if doc.State != domain.FeedbackRunning || len(doc.Steps) != 3 || doc.Steps[0].Status != domain.FeedbackStepPending {
		t.Fatalf("after begin: %+v", doc)
	}
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepStarted})
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepOutput, Line: "ignored"})
	if doc = f.read(t); doc.Steps[0].Status != domain.FeedbackStepRunning || doc.Steps[0].StartedAt == nil {
		t.Fatalf("running: %+v", doc.Steps[0])
	}
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepFail, Summary: "lint failed",
		Findings: []domain.Finding{{Severity: domain.SeverityHigh, Message: "boom"}}}})
	// A pass whose contract names another tree checked another snapshot.
	f.rec.OnStep(application.StepEvent{Step: "test", Phase: application.StepFinished, Result: domain.StepResult{Step: "test", Status: domain.StepPass,
		Contract: &domain.ExecutionContract{TreeSHA: strings.Repeat("9", 40)}}})
	// A step nobody expected is appended.
	f.rec.OnStep(application.StepEvent{Step: "custom", Phase: application.StepFinished, Result: domain.StepResult{Step: "custom", Status: domain.StepPass,
		Contract: &domain.ExecutionContract{TreeSHA: f.snap.Tree}}})
	f.rec.Complete("staged checks failed")
	doc = f.read(t)
	if doc.State != domain.FeedbackComplete || doc.EvaluatedAt == nil || doc.Message != "staged checks failed" {
		t.Fatalf("complete: %+v", doc)
	}
	names := []domain.StepName{}
	for _, s := range doc.Steps {
		names = append(names, s.Step)
	}
	if len(doc.Steps) != 2 || doc.Steps[0].Status != domain.FeedbackStepFail || doc.Steps[0].StartedAt == nil ||
		doc.Steps[0].Findings[0].Source != domain.FindingSourceOutput || doc.Steps[1].Step != "custom" {
		t.Fatalf("steps after complete (pending and foreign-tree entries dropped): %v %+v", names, doc.Steps)
	}

	// Events of a cancelled evaluation never land.
	cancel()
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepPass}})
	if f.read(t).Steps[0].Status != domain.FeedbackStepFail {
		t.Fatal("a cancelled evaluation changed the document")
	}
}

// When the snapshot changes, the step list is replaced: an old result is
// never shown under a new snapshot id.
func TestRecorderNeverCarriesResultsToANewSnapshot(t *testing.T) {
	f := newRecorderFixture(t)
	f.rec.Begin(context.Background(), f.snap, []domain.StepName{"test"})
	f.rec.OnStep(application.StepEvent{Step: "test", Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepPass}})
	f.rec.Complete("")
	next := domain.NewFeedbackSnapshot(strings.Repeat("e", 40), f.snap.Base, "main", testConfig, "sha256:env1")
	f.rec.Begin(context.Background(), next, []domain.StepName{"test"})
	doc := f.read(t)
	if doc.Snapshot.ID != next.ID || doc.State != domain.FeedbackRunning || doc.EvaluatedAt != nil ||
		len(doc.Steps) != 1 || doc.Steps[0].Status != domain.FeedbackStepPending {
		t.Fatalf("after the snapshot moved: %+v", doc)
	}
}

func TestRecorderTransitions(t *testing.T) {
	f := newRecorderFixture(t)
	f.rec.Begin(context.Background(), f.snap, []domain.StepName{"lint", "test"})
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepStarted})

	f.rec.Paused("a gate run is in progress")
	doc := f.read(t)
	if doc.State != domain.FeedbackPending || doc.Steps[0].Status != domain.FeedbackStepPending || doc.Steps[0].StartedAt != nil {
		t.Fatalf("paused: %+v", doc)
	}
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepPass}})
	if f.read(t).Steps[0].Status != domain.FeedbackStepPending {
		t.Fatal("an event after the pause landed")
	}

	f.rec.Begin(context.Background(), f.snap, []domain.StepName{"lint", "test"})
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepFail, Blocker: domain.BlockerEnvironment}})
	f.rec.OnStep(application.StepEvent{Step: "test", Phase: application.StepStarted})
	f.rec.Blocked("container execution unavailable")
	doc = f.read(t)
	if doc.State != domain.FeedbackBlocked || doc.EvaluatedAt != nil || len(doc.Steps) != 2 ||
		doc.Steps[0].Blocker != domain.BlockerEnvironment || doc.Steps[1].Status != domain.FeedbackStepPending {
		t.Fatalf("blocked: %+v", doc)
	}

	f.rec.Begin(context.Background(), f.snap, []domain.StepName{"lint"})
	f.rec.Idle("nothing staged")
	doc = f.read(t)
	if doc.State != domain.FeedbackIdle || len(doc.Steps) != 0 || doc.EvaluatedAt == nil {
		t.Fatalf("idle: %+v", doc)
	}

	f.rec.EnvironmentObserved("")
	f.rec.EnvironmentObserved("sha256:env1")
	if f.read(t).Freshness != "" {
		t.Fatal("an unchanged environment marked the document stale")
	}
	f.rec.EnvironmentObserved("sha256:env2")
	if f.read(t).Freshness != domain.FreshnessStaleEnvironment {
		t.Fatal("a changed environment was not recorded")
	}
	f.rec.EnvironmentObserved("sha256:env1")
	if f.read(t).Freshness != "" {
		t.Fatal("returning to the evaluated environment kept the stale mark")
	}

	f.rec.Begin(context.Background(), f.snap, []domain.StepName{"lint"})
	f.rec.OnStep(application.StepEvent{Step: "lint", Phase: application.StepStarted})
	if err := f.rec.Stop(); err != nil {
		t.Fatal(err)
	}
	doc = f.read(t)
	if doc.State != domain.FeedbackStopped || doc.Steps[0].Status != domain.FeedbackStepPending {
		t.Fatalf("stopped: %+v", doc)
	}
	if got := f.rec.Document(); got.State != domain.FeedbackStopped {
		t.Fatalf("document copy: %+v", got)
	}
}

func TestRecorderHeartbeatReportsAndRetriesFailedWrites(t *testing.T) {
	f := newRecorderFixture(t)
	f.rec.Begin(context.Background(), f.snap, nil)
	first := f.read(t).Producer.HeartbeatAt
	time.Sleep(5 * time.Millisecond)
	if err := f.rec.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	if !f.read(t).Producer.HeartbeatAt.After(first) {
		t.Fatal("heartbeat did not advance")
	}
	// Make the directory unwritable by replacing it with a file.
	dir := filepath.Dir(f.path)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.rec.Heartbeat(); err == nil {
		t.Fatal("a failed write was not reported")
	}
}

// The recorder is an Observer, called concurrently during a parallel batch.
func TestRecorderConcurrentEvents(t *testing.T) {
	f := newRecorderFixture(t)
	f.rec.Begin(context.Background(), f.snap, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			name := domain.StepName("step" + string(rune('a'+i)))
			f.rec.OnStep(application.StepEvent{Step: name, Phase: application.StepStarted})
			f.rec.OnStep(application.StepEvent{Step: name, Phase: application.StepFinished, Result: domain.StepResult{Status: domain.StepPass}})
		})
	}
	wg.Wait()
	f.rec.Complete("")
	if doc := f.read(t); len(doc.Steps) != 8 {
		t.Fatalf("steps: %d", len(doc.Steps))
	}
}
