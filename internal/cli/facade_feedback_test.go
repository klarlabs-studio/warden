package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	mcpclient "go.klarlabs.de/mcp"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// The agent surfaces read through the same reader: a missing document is an
// answer, an observation carries the reader's freshness, and an invalid
// document is an error the agent can read.
func TestFeedbackAgentSurfaces(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, failingWatchConfig)
	installFakeDocker(t, fakeDocker{failRun: true})

	var out, errb bytes.Buffer
	if code := cmdAxi([]string{"feedback"}, &out, &errb); code != 1 || !strings.Contains(out.String(), "present: false") {
		t.Fatalf("axi without a document: %d %s %s", code, out.String(), errb.String())
	}
	f, err := newFacade()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.FeedbackStatus(); err != nil || got.Present || !strings.Contains(got.Message, "warden watch --staged") {
		t.Fatalf("facade without a document: %+v %v", got, err)
	}

	stageFile(t, dir, "sample", "staged\n")
	_ = cmdWatch([]string{"--staged", "--once"}, &out, &errb)
	got, err := f.FeedbackStatus()
	if err != nil || !got.Present || !got.Diagnostics || got.Document == nil || got.Document.Freshness != domain.FreshnessCurrent {
		t.Fatalf("facade: %+v %v", got, err)
	}
	out.Reset()
	errb.Reset()
	if code := cmdAxi([]string{"feedback"}, &out, &errb); code != 0 {
		t.Fatalf("axi: %d %s", code, errb.String())
	}
	for _, want := range []string{"freshness: current", "diagnostics: true", "state: stopped", "status: fail", "{message,severity,source}", "\"main.go:3: error: boom\",high,output"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("axi output lacks %q:\n%s", want, out.String())
		}
	}
	if code := cmdAxi([]string{"feedback", "extra"}, &out, &errb); code != 2 {
		t.Fatalf("axi extra argument: %d", code)
	}
	if code := cmdAxi([]string{"feedback", "--nope"}, &out, &errb); code != 2 {
		t.Fatalf("axi unknown flag: %d", code)
	}

	stageFile(t, dir, "sample", "restaged\n")
	if got, _ = f.FeedbackStatus(); got.Diagnostics || got.Document.Freshness != domain.FreshnessStaleSnapshot || !strings.Contains(got.FreshnessNote, "withheld") {
		t.Fatalf("stale facade: %+v", got)
	}

	if err := os.WriteFile(statusPath(t), []byte(`{"schema":"warden.feedback/v0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.FeedbackStatus()
	var visible *mcpclient.ToolInputError
	if err == nil || !errors.As(err, &visible) || !strings.Contains(visible.Message, "invalid feedback document") {
		t.Fatalf("invalid document: %v", err)
	}
	if code := cmdAxi([]string{"feedback"}, &out, &errb); code != 1 {
		t.Fatalf("axi invalid document: %d", code)
	}
}

// The reason TestFeedbackAgentSurfaces pins the machine reading: on an
// oversubscribed box the same failure gains a why column carrying the load
// caveat. That is the caveat working, not a regression — so the ordinary shape
// is asserted on an idle reading, and the loaded shape here, on a forced one,
// instead of either depending on whatever the machine running the suite was
// doing at the time.
func TestFeedbackAgentSurfaces_LoadCaveatOnlyAddsAWhyColumn(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, failingWatchConfig)
	installFakeDocker(t, fakeDocker{failRun: true})
	forceLoad(t, proc.Load{Known: true, Value: 109, CPUs: 10})

	var out, errb bytes.Buffer
	stageFile(t, dir, "sample", "staged\n")
	_ = cmdWatch([]string{"--staged", "--once"}, &out, &errb)
	out.Reset()
	errb.Reset()
	if code := cmdAxi([]string{"feedback"}, &out, &errb); code != 0 {
		t.Fatalf("axi: %d %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"status: fail", "{message,severity,source,why}", "\"main.go:3: error: boom\",high,output,", "10.9x oversubscribed", "verdict above stands"} {
		if !strings.Contains(got, want) {
			t.Errorf("axi output lacks %q:\n%s", want, got)
		}
	}
}
