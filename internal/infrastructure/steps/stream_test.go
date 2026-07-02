package steps

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestLineWriter_SplitsLinesAndFlushesPartial(t *testing.T) {
	var got []string
	w := newLineWriter(func(l string) { got = append(got, l) })

	// Lines can arrive split across writes; a line is emitted only once complete.
	w.Write([]byte("hello\nwor"))
	w.Write([]byte("ld\n"))
	w.Write([]byte("partial")) // no newline yet — not emitted until Close
	if want := []string{"hello", "world"}; !reflect.DeepEqual(got, want) {
		t.Errorf("mid-stream lines = %v, want %v", got, want)
	}
	w.Close()
	if want := []string{"hello", "world", "partial"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after Close = %v, want %v", got, want)
	}
}

func TestShellStep_StreamsOutputLive(t *testing.T) {
	var lines []string
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"test": "printf 'line-a\\nline-b\\n'"},
		OnOutput:    func(l string) { lines = append(lines, l) },
	}
	res, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass", res.Status)
	}
	if len(lines) != 2 || lines[0] != "line-a" || lines[1] != "line-b" {
		t.Errorf("streamed lines = %v, want [line-a line-b]", lines)
	}
}

func TestShellStep_FailingCommandStillCapturesOutput(t *testing.T) {
	var lines []string
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": "echo boom; exit 3"},
		OnOutput:    func(l string) { lines = append(lines, l) },
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	// The streamed line is also retained in the failure finding.
	if len(res.Findings) != 1 || res.Findings[0].Message != "boom" {
		t.Errorf("finding = %+v, want the captured output", res.Findings)
	}
	if len(lines) != 1 || lines[0] != "boom" {
		t.Errorf("streamed lines = %v, want [boom]", lines)
	}
}

func TestShellStep_NoOutputSink_StillRuns(t *testing.T) {
	// The non-interactive path (OnOutput nil) must keep working unchanged.
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"test": "echo ok"},
	}
	res, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass", res.Status)
	}
}

func TestShellStep_TimeoutReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"test": "sleep 5"},
		Timeout:     50 * time.Millisecond, // used only for the message string
	}
	res, err := NewShellStep(domain.StepTest, "test").Run(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail on timeout", res.Status)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "timed out") {
		t.Errorf("expected a timeout finding, got %+v", res.Findings)
	}
}
