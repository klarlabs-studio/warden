package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestWatchStagedInvocation(t *testing.T) {
	for _, args := range [][]string{{"--once"}, {"--unknown"}, {"extra"}} {
		var out, errb bytes.Buffer
		if code := cmdWatch(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	repoWithConfig(t, "commands: { test: 'true' }\n")
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
	bin := t.TempDir()
	imageID := "sha256:" + strings.Repeat("a", 64)
	script := `#!/bin/sh
case "$1" in
info) exit 0;;
image) printf '%s' '[{"Id":"` + imageID + `","Os":"linux","Architecture":"arm64"}]';;
run) echo checked; exit 0;;
rm) exit 0;;
*) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
