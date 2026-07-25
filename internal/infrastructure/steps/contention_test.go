package steps

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestIsContention(t *testing.T) {
	contending := []string{
		"Error: parallel golangci-lint is running",
		"ERROR: PARALLEL GOLANGCI-LINT IS RUNNING", // matched case-insensitively
		"Blocking waiting for file lock on build directory",
		"another instance is already running",
		"could not acquire lock on /tmp/x",
	}
	for _, out := range contending {
		if !isContention(out) {
			t.Errorf("isContention(%q) = false, want true", out)
		}
	}

	// A real finding must never be mistaken for contention — that would retry a
	// genuine failure and then soften it into "could not run".
	notContending := []string{
		"",
		"main.go:12:2: undefined: foo",
		"FAIL\tgo.klarlabs.de/warden/internal/cli\t0.4s",
		"level=error msg=\"running is required\"",
		"lock the mutex before reading the map", // a lint message *about* locks
	}
	for _, out := range notContending {
		if isContention(out) {
			t.Errorf("isContention(%q) = true, want false", out)
		}
	}
}

// A tool that declines to start because a sibling holds its lock has not
// answered "is this tree clean". Warden must wait it out rather than report a
// lint error that does not exist.
func TestShellStep_RetriesThroughLockContention(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "lock-released")

	// Refuse exactly like golangci-lint does until the marker appears; the second
	// attempt finds it and succeeds.
	script := `if [ ! -f ` + marker + ` ]; then
  touch ` + marker + `
  echo "Error: parallel golangci-lint is running" >&2
  exit 1
fi
exit 0`

	var lines []string
	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"lint": script},
		OnOutput:    func(l string) { lines = append(lines, l) },
	}

	start := time.Now()
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%s), want pass after the lock cleared", res.Status, res.Summary)
	}
	// It really waited rather than passing by accident on the first attempt.
	if elapsed := time.Since(start); elapsed < contentionPoll {
		t.Errorf("returned in %s, expected at least one %s poll", elapsed, contentionPoll)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "holds lint's lock, waiting") {
		t.Errorf("developer was not told why the step paused: %q", lines)
	}
}

// The notice must be emitted once, not on every poll — a wall of identical
// lines is its own kind of noise.
func TestShellStep_ContentionNoticeIsEmittedOnce(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "n")
	script := `n=$(cat ` + counter + ` 2>/dev/null || echo 0)
n=$((n+1)); echo $n > ` + counter + `
if [ "$n" -lt 3 ]; then echo "Error: parallel golangci-lint is running" >&2; exit 1; fi
exit 0`

	var notices int
	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"lint": script},
		OnOutput: func(l string) {
			if strings.Contains(l, "waiting…") {
				notices++
			}
		},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%s), want pass", res.Status, res.Summary)
	}
	if notices != 1 {
		t.Errorf("emitted %d waiting notices across 2 retries, want exactly 1", notices)
	}
}

// A genuine failure must fail immediately and keep saying "failed" — the
// contention path must never launder a real lint error into "could not run".
func TestShellStep_RealFailureIsNotTreatedAsContention(t *testing.T) {
	dir := t.TempDir()
	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"lint": `echo "main.go:1:1: undefined: foo"; exit 1`},
	}
	start := time.Now()
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if res.Summary != "lint failed" {
		t.Errorf("Summary = %q, want the ordinary failure wording", res.Summary)
	}
	if elapsed := time.Since(start); elapsed >= contentionPoll {
		t.Errorf("a real failure waited %s — it must not be retried", elapsed)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "undefined: foo") {
		t.Errorf("the tool's own output must survive: %+v", res.Findings)
	}
}

// Exhausting the budget still fails the gate — "I could not check" is not "the
// tree is clean" — but the wording must point at the lock, not at the code.
func TestShellStep_PersistentContentionFailsHonestly(t *testing.T) {
	// Shrink the budget so the test does not wait a real minute.
	origBudget, origPoll := contentionBudget, contentionPoll
	contentionBudget, contentionPoll = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { contentionBudget, contentionPoll = origBudget, origPoll })

	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": `echo "Error: parallel golangci-lint is running" >&2; exit 1`},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail — an unverified tree must not pass", res.Status)
	}
	if !strings.Contains(res.Summary, "could not run") {
		t.Errorf("Summary = %q, want it to name contention rather than a lint error", res.Summary)
	}
	msg := res.Findings[0].Message
	if !strings.Contains(msg, "Nothing is wrong with your tree") {
		t.Errorf("message must not send the reader hunting a nonexistent lint error: %q", msg)
	}
}

// A cancelled run must surface as cancellation, not as contention.
func TestShellStep_CancellationBeatsContention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": `echo "Error: parallel golangci-lint is running" >&2; exit 1`},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if strings.Contains(res.Summary, "could not run") {
		t.Errorf("a cancelled run must not be reported as lock contention: %q", res.Summary)
	}
}
