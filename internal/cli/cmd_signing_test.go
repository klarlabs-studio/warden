package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/notify"
	"go.klarlabs.de/warden/internal/service"
)

var recStub = domain.RunRecord{RunID: "run_x", StepsRun: []domain.StepName{"lint"}}

func TestKeyShow(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)

	code, out, errb := run("key", "show")
	if code != 0 {
		t.Fatalf("key show: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "fingerprint:") || !strings.Contains(out, "public key:") {
		t.Errorf("key show output missing key material:\n%s", out)
	}
	if !strings.Contains(out, "trusted_keys:") {
		t.Errorf("key show should point at the .warden.yaml roster:\n%s", out)
	}

	// Wrong subcommand → usage, exit 2.
	if code, _, errb := run("key", "bogus"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("key bad subcommand: code=%d err=%q", code, errb)
	}
}

func TestVerify_KeyFlagParses(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)

	// No note on HEAD, but --key must parse and drive the pinned path.
	code, out, _ := run("verify", "--key", "deadbeefdeadbeef")
	if code != 1 {
		t.Errorf("verify with no note should exit 1, got %d", code)
	}
	if !strings.Contains(out, "unverified") {
		t.Errorf("expected unverified output, got %q", out)
	}
}

func TestPrintVerify_SignatureBranches(t *testing.T) {
	cases := []struct {
		name   string
		res    service.VerifyResult
		pinned bool
		want   string
	}{
		{
			name: "trusted validated",
			res:  service.VerifyResult{Validated: true, Record: &recStub, Signed: true, SignatureValid: true, Trusted: true, Signer: "abc123"},
			want: "signed by trusted abc123",
		},
		{
			name: "validated signed but not pinned",
			res:  service.VerifyResult{Validated: true, Record: &recStub, Signed: true, SignatureValid: true, Signer: "abc123"},
			want: "signed by abc123",
		},
		{
			name: "validated unsigned",
			res:  service.VerifyResult{Validated: true, Record: &recStub},
			want: "unsigned",
		},
		{
			name:   "pinned untrusted signer",
			res:    service.VerifyResult{Record: &recStub, Signed: true, SignatureValid: true, Trusted: false, Signer: "abc123"},
			pinned: true,
			want:   "untrusted key abc123",
		},
		{
			name:   "pinned invalid signature",
			res:    service.VerifyResult{Record: &recStub, Signed: true, SignatureValid: false},
			pinned: true,
			want:   "signature does not verify",
		},
		{
			// Record is what makes this an UNSIGNED NOTE rather than NO note.
			// Without it this case asserted the unsigned-note message about a
			// result that describes an absent one, which is how the defect below
			// survived: the fixture was built from the same wrong model as the
			// code, so it agreed with it.
			name:   "pinned but unsigned",
			res:    service.VerifyResult{Record: &recStub, Signed: false},
			pinned: true,
			want:   "unsigned but a trusted key was required",
		},
		{
			// No note at all. `Signed` is false here too, so with a pinned roster
			// this was reported as "note is unsigned", sending the reader to look
			// for a note that does not exist.
			name:   "pinned, no note at all",
			res:    service.VerifyResult{},
			pinned: true,
			want:   "no warden note",
		},
		{
			// …and the same mistake unpinned, where nothing would have caught it.
			name: "unpinned, no note at all",
			res:  service.VerifyResult{},
			want: "no warden note",
		},
		{
			// The mirror image: a note IS there, it just does not attest this
			// commit. Reporting "no intact warden note" about it is the same
			// wrong diagnosis pointing the other way.
			name: "note exists but does not attest",
			res:  service.VerifyResult{Record: &recStub, Signed: true, SignatureValid: true},
			want: "does not attest this commit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printVerify(&b, tc.res, tc.pinned)
			if !strings.Contains(b.String(), tc.want) {
				t.Errorf("printVerify output %q missing %q", b.String(), tc.want)
			}
		})
	}
}

func TestWhy_NoNote(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	gitRepo(t)

	// A commit with no warden note explains itself as un-gated, exit 1.
	code, out, _ := run("why")
	if code != 1 {
		t.Errorf("why on an un-gated commit should exit 1, got %d", code)
	}
	if !strings.Contains(out, "no warden note") {
		t.Errorf("expected the no-note explanation, got %q", out)
	}
}

func TestWhy_WithNote(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)

	// Write a provenance note on HEAD via a service, then explain it.
	svc, err := service.New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	rec := domain.RunRecord{
		RunID:             "run_why",
		CommitSHA:         head,
		WardenVersion:     "9.9.9",
		StepsRun:          []domain.StepName{"test", "lint"},
		Agent:             map[domain.StepName]string{"test": "claude"},
		MatchedRules:      []string{"main"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
	}
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}

	code, out, errb := run("why")
	if code != 0 {
		t.Fatalf("why with a note: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"run_why", "9.9.9", "test(agent=claude)", "lint", "main", "chain intact", "unsigned"} {
		if !strings.Contains(out, want) {
			t.Errorf("why output missing %q:\n%s", want, out)
		}
	}
}

// fakeCfgSvc feeds maybeNotify a scripted config.
type fakeCfgSvc struct {
	cfg domain.Config
	err error
}

func (f fakeCfgSvc) Config() (domain.Config, error) { return f.cfg, f.err }

// Repo returns nil: maybeNotify treats repo context as decoration, so the
// notification must still fire without it.
func (f fakeCfgSvc) Repo() *git.Repo { return nil }

// captureNotifications swaps the send seam for the duration of a test and
// returns the notifications maybeNotify actually emitted.
func captureNotifications(t *testing.T) *[]notify.Notification {
	t.Helper()
	var sent []notify.Notification
	orig := sendNotification
	sendNotification = func(n notify.Notification) { sent = append(sent, n) }
	t.Cleanup(func() { sendNotification = orig })
	return &sent
}

func TestMaybeNotify_RespectsConfigAndVerdict(t *testing.T) {
	off := false
	cases := []struct {
		name     string
		svc      fakeCfgSvc
		res      application.RunResult
		elapsed  time.Duration
		wantSend bool
		wantBody string
	}{
		{
			name:    "notify:false stays silent",
			svc:     fakeCfgSvc{cfg: domain.Config{Notify: &off}},
			res:     application.RunResult{Outcome: domain.OutcomePassed},
			elapsed: notifyAfter, wantSend: false,
		},
		{
			name:    "slow pass notifies",
			svc:     fakeCfgSvc{cfg: domain.Config{}},
			res:     application.RunResult{Outcome: domain.OutcomePassed, Message: "pushed"},
			elapsed: notifyAfter, wantSend: true, wantBody: "pushed",
		},
		{
			// A blocked push always notifies, however fast it failed — you never
			// want to miss the gate that stopped you.
			name:    "failure notifies even when fast",
			svc:     fakeCfgSvc{cfg: domain.Config{}},
			res:     application.RunResult{Outcome: domain.OutcomeFailed, Message: "blocked"},
			elapsed: 0, wantSend: true, wantBody: "blocked",
		},
		{
			name:    "fast pass stays silent",
			svc:     fakeCfgSvc{cfg: domain.Config{}},
			res:     application.RunResult{Outcome: domain.OutcomePassed, Message: "quick"},
			elapsed: notifyAfter - time.Millisecond, wantSend: false,
		},
		{
			// An unreadable config must not turn a finished run into a crash, and
			// must not guess: no config, no notification.
			name:    "config error stays silent",
			svc:     fakeCfgSvc{err: errSentinel},
			res:     application.RunResult{Outcome: domain.OutcomeFailed, Message: "blocked"},
			elapsed: notifyAfter, wantSend: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sent := captureNotifications(t)
			maybeNotify(c.svc, c.res, c.elapsed)

			if got := len(*sent) > 0; got != c.wantSend {
				t.Fatalf("notified = %v, want %v", got, c.wantSend)
			}
			if c.wantSend && (*sent)[0].Body != c.wantBody {
				t.Errorf("body = %q, want %q", (*sent)[0].Body, c.wantBody)
			}
		})
	}
}

var errSentinel = fmt.Errorf("boom")

func TestRecipes(t *testing.T) {
	// List shows every recipe name.
	code, out, _ := run("recipes")
	if code != 0 || !strings.Contains(out, "gitleaks") || !strings.Contains(out, "coverage-delta") {
		t.Errorf("recipes list wrong: code=%d out=%q", code, out)
	}
	// A named recipe prints its snippet.
	code, out, _ = run("recipes", "gitleaks")
	if code != 0 || !strings.Contains(out, "gitleaks detect") || !strings.Contains(out, "commands:") {
		t.Errorf("recipe snippet wrong: code=%d out=%q", code, out)
	}
	// Unknown recipe errors.
	if code, _, errb := run("recipes", "nope"); code != 1 || !strings.Contains(errb, "no recipe") {
		t.Errorf("unknown recipe: code=%d err=%q", code, errb)
	}
}

func TestWatchCommands(t *testing.T) {
	cfg := domain.Config{
		Steps:    map[string][]domain.StepName{"pre_commit": {"lint", "review", "test"}},
		Commands: map[string]string{"lint": "golangci-lint run", "test": "go test ./..."},
	}
	got := watchCommands(cfg)
	// review has no command → skipped; lint + test resolve.
	if len(got) != 2 {
		t.Fatalf("expected 2 watch commands, got %d: %+v", len(got), got)
	}
	if got[0].step != "lint" || got[0].command != "golangci-lint run" {
		t.Errorf("first watch command wrong: %+v", got[0])
	}
	if got[1].step != "test" {
		t.Errorf("second watch command wrong: %+v", got[1])
	}
}

func TestRunWatchChecks_PassAndFail(t *testing.T) {
	var b strings.Builder
	runWatchChecks(context.Background(), &b, t.TempDir(), []namedCommand{
		{step: "ok", command: "true"},
		{step: "bad", command: "echo boom >&2; false"},
	})
	out := b.String()
	if !strings.Contains(out, "✓ ok") || !strings.Contains(out, "✗ bad") {
		t.Errorf("watch check output wrong:\n%s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("a failing check must print its output:\n%s", out)
	}
}

func TestWatch_NoCommandsExits1(t *testing.T) {
	dir := gitRepo(t)
	// A config with a pre_commit step but no command → nothing to watch, exit 1.
	writeConfig(t, dir, "steps: { pre_commit: [lint] }\n")
	code, _, errb := run("watch")
	if code != 1 || !strings.Contains(errb, "no pre-commit commands") {
		t.Errorf("watch with no commands: code=%d err=%q", code, errb)
	}
}

func TestAttach_NoLiveRun(t *testing.T) {
	gitRepo(t)
	code, _, errb := run("attach")
	if code != 1 || !strings.Contains(errb, "no live run") {
		t.Errorf("attach with no run: code=%d err=%q", code, errb)
	}
}
