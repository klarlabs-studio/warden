package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
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
	if !strings.Contains(out, "warden verify --key") {
		t.Errorf("key show should suggest the pinning command:\n%s", out)
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
			res:    service.VerifyResult{Signed: true, SignatureValid: true, Trusted: false, Signer: "abc123"},
			pinned: true,
			want:   "untrusted key abc123",
		},
		{
			name:   "pinned invalid signature",
			res:    service.VerifyResult{Signed: true, SignatureValid: false},
			pinned: true,
			want:   "signature does not verify",
		},
		{
			name:   "pinned but unsigned",
			res:    service.VerifyResult{Signed: false},
			pinned: true,
			want:   "unsigned but a trusted key was required",
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

func TestMaybeNotify_RespectsConfigAndVerdict(t *testing.T) {
	// These calls exercise the gating logic; notify.Send is a best-effort no-op
	// on the test host, so we assert no panic and the right branches run.
	off := false
	maybeNotify(fakeCfgSvc{cfg: domain.Config{Notify: &off}}, application.RunResult{Outcome: domain.OutcomePassed})

	maybeNotify(fakeCfgSvc{cfg: domain.Config{}}, application.RunResult{Outcome: domain.OutcomePassed, Message: "pushed"})
	maybeNotify(fakeCfgSvc{cfg: domain.Config{}}, application.RunResult{Outcome: domain.OutcomeFailed, Message: "blocked"})

	// A config load error must not panic.
	maybeNotify(fakeCfgSvc{err: errSentinel}, application.RunResult{})
}

var errSentinel = fmt.Errorf("boom")
