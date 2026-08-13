package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/scanner"
)

// A verdict that arrives minutes later, on a machine with several repos open,
// has to say which repo, which branch, and what to do next.
func TestBuildNotification(t *testing.T) {
	t.Run("a failure names the hook, scope and failing step", func(t *testing.T) {
		n := buildNotification(application.RunResult{
			Hook:    domain.PrePush,
			Outcome: domain.OutcomeFailed,
			Message: "step lint failed",
		}, "warden", "feat/gate-reporting")

		if n.Title != "warden: pre-push failed" {
			t.Errorf("Title = %q", n.Title)
		}
		if n.Subtitle != "warden · feat/gate-reporting" {
			t.Errorf("Subtitle = %q", n.Subtitle)
		}
		if n.Body != "step lint failed" {
			t.Errorf("Body = %q", n.Body)
		}
		if !n.Urgent {
			t.Error("a blocked push must be urgent — it is the case notifications exist for")
		}
		if n.Group != "warden-warden" {
			t.Errorf("Group = %q, want a per-repo group", n.Group)
		}
	})

	t.Run("a pass names the checks behind it", func(t *testing.T) {
		n := buildNotification(application.RunResult{
			Hook:    domain.PrePush,
			Outcome: domain.OutcomePassed,
			Message: "pushed 2 commits",
			Policy:  domain.ResolvedPolicy{Steps: []domain.StepName{"lint", "test"}},
		}, "warden", "main")

		if n.Body != "pushed 2 commits (lint, test)" {
			t.Errorf("Body = %q", n.Body)
		}
		if n.Urgent {
			t.Error("a passing run must not be urgent")
		}
	})

	t.Run("missing repo context costs the subtitle, not the notification", func(t *testing.T) {
		n := buildNotification(application.RunResult{
			Hook: domain.PreCommit, Outcome: domain.OutcomeFailed, Message: "step test failed",
		}, "", "")
		if n.Subtitle != "" {
			t.Errorf("Subtitle = %q, want empty", n.Subtitle)
		}
		if n.Title == "" || n.Body == "" {
			t.Errorf("notification must still be usable: %+v", n)
		}
	})

	t.Run("branch alone still scopes the verdict", func(t *testing.T) {
		if got := buildNotification(application.RunResult{Outcome: domain.OutcomeFailed}, "", "main").Subtitle; got != "main" {
			t.Errorf("Subtitle = %q, want main", got)
		}
		if got := buildNotification(application.RunResult{Outcome: domain.OutcomeFailed}, "repo", "").Subtitle; got != "repo" {
			t.Errorf("Subtitle = %q, want repo", got)
		}
	})

	t.Run("an empty message falls back to the verdict", func(t *testing.T) {
		n := buildNotification(application.RunResult{Hook: domain.PrePush, Outcome: domain.OutcomeRejected}, "r", "b")
		if n.Body != "rejected" {
			t.Errorf("Body = %q, want the verdict as a floor", n.Body)
		}
	})
}

// A split policy that keeps pre-commit fast must not let the pass line imply
// the whole tree is green — the deferred steps have to be named in the same
// breath as the ones that passed.
func TestPassLine(t *testing.T) {
	tests := []struct {
		name    string
		ran     []domain.StepName
		prePush []domain.StepName
		want    string
	}{
		{
			name:    "split policy names what ran and what is deferred",
			ran:     []domain.StepName{"lint"},
			prePush: []domain.StepName{"test", "lint"},
			want:    "warden: pre-commit passed (lint) — test runs at pre-push.",
		},
		{
			name:    "several deferred steps are listed in pre-push order",
			ran:     []domain.StepName{"lint"},
			prePush: []domain.StepName{"rebase", "lint", "security-scan", "test"},
			want:    "warden: pre-commit passed (lint) — rebase, security-scan, test run at pre-push.",
		},
		{
			name:    "nothing deferred keeps the line short",
			ran:     []domain.StepName{"lint", "test"},
			prePush: []domain.StepName{"lint"},
			want:    "warden: pre-commit passed (lint, test).",
		},
		{
			name:    "an unreadable pre-push list still reports the pass",
			ran:     []domain.StepName{"lint"},
			prePush: nil,
			want:    "warden: pre-commit passed (lint).",
		},
		{
			name:    "an unknown step list degrades to the unqualified line",
			ran:     nil,
			prePush: []domain.StepName{"test"},
			want:    "warden: pre-commit passed.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := passLine(tc.ran, tc.prePush); got != tc.want {
				t.Errorf("passLine() =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// `warden watch` should surface exactly where the split policy leaves a gap,
// and stay quiet everywhere else.
func TestWatchTip(t *testing.T) {
	armed := map[domain.Hook]bool{domain.PreCommit: true, domain.PrePush: true}

	got := watchTip(armed, []domain.StepName{"lint"}, []domain.StepName{"test", "lint"})
	if !strings.Contains(got, "warden watch") || !strings.Contains(got, "(test)") {
		t.Errorf("deferred step should surface watch: %q", got)
	}

	// Nothing deferred → the tip is noise.
	if got := watchTip(armed, []domain.StepName{"lint", "test"}, []domain.StepName{"lint"}); got != "" {
		t.Errorf("nothing deferred should be silent, got %q", got)
	}

	// With no pre-commit shim there is nothing to defer *from*.
	pushOnly := map[domain.Hook]bool{domain.PrePush: true}
	if got := watchTip(pushOnly, []domain.StepName{"lint"}, []domain.StepName{"test", "lint"}); got != "" {
		t.Errorf("no armed pre-commit should be silent, got %q", got)
	}
}

// The hook pin is a bootstrap floor, not a lock: a PATH binary wins. Status has
// to say so when the two disagree, and stay quiet when they don't.
func TestPinSkewLine(t *testing.T) {
	skew := pinSkewLine(map[domain.Hook]string{
		domain.PreCommit: "0.17.0",
		domain.PrePush:   "0.17.0",
	}, "0.18.16")
	// The remedy must name a command that exists and means what it says. It used
	// to say `hooks enable <hook>`, which is the right effect under a verb that
	// describes arming (#212 §6).
	for _, want := range []string{"pre-commit pins 0.17.0", "pre-push pins 0.17.0", "0.18.16 is what runs", "warden hooks repin"} {
		if !strings.Contains(skew, want) {
			t.Errorf("skew line %q missing %q", skew, want)
		}
	}

	// Matching pin → silence.
	if got := pinSkewLine(map[domain.Hook]string{domain.PreCommit: "0.18.16"}, "0.18.16"); got != "" {
		t.Errorf("matching pin should be silent, got %q", got)
	}
	// No hooks installed / no pin recorded → silence.
	if got := pinSkewLine(nil, "0.18.16"); got != "" {
		t.Errorf("absent pins should be silent, got %q", got)
	}
	// Only the diverging hook is named.
	got := pinSkewLine(map[domain.Hook]string{
		domain.PreCommit: "0.18.16",
		domain.PrePush:   "0.17.0",
	}, "0.18.16")
	if strings.Contains(got, "pre-commit") || !strings.Contains(got, "pre-push pins 0.17.0") {
		t.Errorf("only the skewed hook should be named: %q", got)
	}
}

// fakeScanSvc feeds scannerPinLine a scripted config and repo root.
type fakeScanSvc struct {
	cfg  domain.Config
	repo *git.Repo
}

func (f fakeScanSvc) Config() (domain.Config, error) { return f.cfg, nil }
func (f fakeScanSvc) Repo() *git.Repo                { return f.repo }

// A control that reports nothing when it did not run is the failure shape this
// line exists to close: silence must not mean both "versions agree" and "no pin
// found, nothing compared".
func TestScannerPinLine(t *testing.T) {
	noxCmd := map[string]string{"security-scan": "nox scan . -severity-threshold high"}

	t.Run("no recognizable scanner says nothing", func(t *testing.T) {
		dir := t.TempDir()
		svc := fakeScanSvc{cfg: domain.Config{Commands: map[string]string{"security-scan": "make audit"}}, repo: &git.Repo{Dir: dir}}
		if got := scannerPinLine(svc); got != "" {
			t.Errorf("got %q, want silence", got)
		}
	})

	t.Run("an explicitly disabled check says so", func(t *testing.T) {
		off := false
		svc := fakeScanSvc{
			cfg:  domain.Config{Commands: noxCmd, SecurityScan: domain.SecurityScanConfig{VersionCheck: &off}},
			repo: &git.Repo{Dir: t.TempDir()},
		}
		if got := scannerPinLine(svc); !strings.Contains(got, "disabled") {
			t.Errorf("got %q, want it to name the opt-out", got)
		}
	})

	t.Run("no discoverable pin is reported as INERT", func(t *testing.T) {
		// A repo with workflows but no scanner pin — warden's own shape, and the
		// shape of any repo whose pin lives in a shared reusable workflow.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "ci.yml"),
			[]byte("name: CI\non: push\njobs:\n  ci:\n    uses: org/.github/.github/workflows/go-ci.yml@main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := scannerPinLine(fakeScanSvc{cfg: domain.Config{Commands: noxCmd}, repo: &git.Repo{Dir: dir}})
		if !strings.Contains(got, "INERT") {
			t.Errorf("got %q, want it to say the check is inert", got)
		}
		// It must not merely say "inert" — it has to be actionable.
		if !strings.Contains(got, "pin_file") {
			t.Errorf("got %q, want it to name the way out", got)
		}
	})

	t.Run("a matching pin says nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Pin to whatever is actually installed, so this asserts the
		// agree-therefore-silent path rather than a hardcoded version.
		local := scanner.LocalVersion(context.Background(), dir, "nox")
		if local == "" {
			t.Skip("nox not installed")
		}
		if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "ci.yml"),
			[]byte("name: CI\non: push\nenv:\n  NOX_VERSION: \""+local+"\"\njobs:\n  a:\n    runs-on: ubuntu-latest\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := scannerPinLine(fakeScanSvc{cfg: domain.Config{Commands: noxCmd}, repo: &git.Repo{Dir: dir}}); got != "" {
			t.Errorf("got %q, want silence when the versions agree", got)
		}
	})
}

// The TUI renders findings into a frame that is redrawn in place, so a long
// failure output scrolls out of reach and leaves only its tail. The full output
// must therefore also reach plain stdout, where scrollback keeps it (#114).
func TestPrintFindings_CarriesTheWholeFailureOutput(t *testing.T) {
	var buf bytes.Buffer
	msg := "--- FAIL: TestThing (0.02s)\n    thing_test.go:41: got 3, want 4\nFAIL\tpkg/thing\t0.3s"
	printFindings(&buf, []domain.Finding{{Severity: domain.SeverityHigh, Message: msg}})

	out := buf.String()
	// The three things a developer needs to act, none of which survive in the
	// frame's tail: the test name, the assertion, and the package.
	for _, want := range []string{"TestThing", "got 3, want 4", "pkg/thing"} {
		if !strings.Contains(out, want) {
			t.Errorf("printFindings dropped %q:\n%s", want, out)
		}
	}
}

// TestProvenanceMode pins the wording of the line #212 §5 and §9 asked for: the
// repository's provenance mode was invisible, so "signed by an untrusted key"
// was something you learned from a rejection rather than from status.
func TestProvenanceMode(t *testing.T) {
	type key struct{ pub, fp string }
	stub := func(keys []string, k key) interface {
		Config() (domain.Config, error)
		SigningKey() (publicKey, fingerprint string)
	} {
		return provenanceStub{cfg: domain.Config{TrustedKeys: keys}, pub: k.pub, fp: k.fp}
	}

	t.Run("no roster says notes prove a warden ran, not whose", func(t *testing.T) {
		got := provenanceMode(stub(nil, key{pub: "AAAA", fp: "abc"}))
		if !strings.Contains(got, "unsigned") || !strings.Contains(got, "not whose") {
			t.Errorf("got %q", got)
		}
		if !strings.Contains(got, "trusted_keys") {
			t.Error("must name the setting that changes it")
		}
	})

	t.Run("no roster and no key says notes will be unsigned", func(t *testing.T) {
		got := provenanceMode(stub(nil, key{}))
		if !strings.Contains(got, "notes will be unsigned") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("a roster including us reports enforcement", func(t *testing.T) {
		got := provenanceMode(stub([]string{"abc"}, key{pub: "AAAA", fp: "abc"}))
		if !strings.Contains(got, "signed") || !strings.Contains(got, "1 key(s)") {
			t.Errorf("got %q", got)
		}
		if !strings.Contains(got, "including this machine's") {
			t.Errorf("should say our key is on the roster, got %q", got)
		}
	})

	t.Run("a roster excluding us says so", func(t *testing.T) {
		// The case that bit the field report: the gate runs, the notes are
		// written, and verify rejects them. Worth knowing before a push.
		got := provenanceMode(stub([]string{"deadbeef"}, key{pub: "AAAA", fp: "abc"}))
		if !strings.Contains(got, "not on it") {
			t.Errorf("should warn our key is absent from the roster, got %q", got)
		}
	})
}

type provenanceStub struct {
	cfg     domain.Config
	pub, fp string
}

func (p provenanceStub) Config() (domain.Config, error) { return p.cfg, nil }
func (p provenanceStub) SigningKey() (string, string)   { return p.pub, p.fp }

// repin exists to fix a pin, never to change which hooks run. A disabled hook
// must survive it untouched — that is the whole reason it is not `hooks enable`
// (#212 §6).
func TestRepinTargets(t *testing.T) {
	installed := map[domain.Hook]bool{domain.PreCommit: true, domain.PrePush: false}
	pins := map[domain.Hook]string{domain.PreCommit: "0.7.1", domain.PrePush: "0.7.1"}

	got := repinTargets(installed, pins, "0.26.0")
	if len(got) != 1 || got[0] != domain.PreCommit {
		t.Fatalf("repin must skip the disabled hook, got %v", got)
	}

	// A pin that already matches is left alone rather than rewritten.
	if got := repinTargets(installed, map[domain.Hook]string{domain.PreCommit: "0.26.0"}, "0.26.0"); len(got) != 0 {
		t.Errorf("a current pin needs no rewrite, got %v", got)
	}

	// An armed hook whose pin could not be read is repinned: unknown is drift.
	if got := repinTargets(installed, nil, "0.26.0"); len(got) != 1 || got[0] != domain.PreCommit {
		t.Errorf("an unreadable pin should be repinned, got %v", got)
	}

	// Nothing armed, nothing to do — even with stale pins on disk.
	if got := repinTargets(map[domain.Hook]bool{}, pins, "0.26.0"); len(got) != 0 {
		t.Errorf("no armed hooks means no targets, got %v", got)
	}
}
