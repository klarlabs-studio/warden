package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// fakeTTY is a controlling terminal: it answers from in and records what the
// approver wrote to it.
type fakeTTY struct {
	in     io.Reader
	out    bytes.Buffer
	closed bool
}

func (f *fakeTTY) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeTTY) Write(p []byte) (int, error) { return f.out.Write(p) }
func (f *fakeTTY) Close() error                { f.closed = true; return nil }

// Under `git push` stdin is the ref list, not a terminal: the approver asks on
// the controlling terminal, and the prompt goes there, never to stdout.
func TestTerminalApprover_AsksOnTheControllingTerminal(t *testing.T) {
	req := application.ApprovalRequest{Hook: domain.PrePush, Branch: "main", Risk: domain.RiskHigh,
		Findings: []domain.Finding{{Severity: domain.SeverityHigh, File: "x.go", Line: 3, Message: "risky"}}}
	for _, c := range []struct {
		answer string
		want   bool
	}{{"y\n", true}, {"Y\n", true}, {"n\n", false}, {"\n", false}, {"", false}} {
		tty := &fakeTTY{in: strings.NewReader(c.answer)}
		var stdout bytes.Buffer
		a := terminalApprover{
			in: strings.NewReader("refs/heads/main abc refs/heads/main def\n"), out: &stdout,
			openTTY: func() (io.ReadWriteCloser, error) { return tty, nil }, ttyTimeout: time.Minute,
		}
		d, err := a.Approve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved != c.want {
			t.Errorf("answer %q: approved = %v, want %v", c.answer, d.Approved, c.want)
		}
		if c.want && d.Rationale != "controlling terminal decision" {
			t.Errorf("rationale = %q", d.Rationale)
		}
		if !strings.Contains(tty.out.String(), "approve? [y/N]") || !strings.Contains(tty.out.String(), "x.go:3") {
			t.Errorf("the prompt and findings belong on the terminal: %q", tty.out.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("nothing may be written to stdout when the terminal is asked: %q", stdout.String())
		}
		if !tty.closed {
			t.Error("the terminal must be closed after asking")
		}
	}
}

// No controlling terminal (CI, launchd, a process spawned without one): decline
// exactly as before, and say what works.
func TestTerminalApprover_DeclinesWithoutAControllingTerminal(t *testing.T) {
	var stdout bytes.Buffer
	a := terminalApprover{
		in: strings.NewReader(""), out: &stdout,
		openTTY: func() (io.ReadWriteCloser, error) { return nil, errors.New("no tty") },
	}
	d, err := a.Approve(context.Background(), application.ApprovalRequest{Hook: domain.PrePush, Profile: profileRef("standard@1")})
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved || d.Rationale != "no tty" {
		t.Fatalf("want a no-tty decline, got %+v", d)
	}
	if !strings.Contains(stdout.String(), "running `git push` from an interactive terminal") {
		t.Errorf("the hint must name what works: %q", stdout.String())
	}
}

// A terminal nobody answers declines after the timeout instead of hanging the
// push; a cancelled run declines at once.
func TestTerminalApprover_UnansweredTerminalDeclines(t *testing.T) {
	block, unblock := io.Pipe()
	defer func() { _ = unblock.Close() }()
	tty := &fakeTTY{in: block}
	a := terminalApprover{
		in: strings.NewReader(""), out: io.Discard,
		openTTY: func() (io.ReadWriteCloser, error) { return tty, nil }, ttyTimeout: 10 * time.Millisecond,
	}
	if d, _ := a.Approve(context.Background(), application.ApprovalRequest{Hook: domain.PrePush}); d.Approved || d.Rationale != "no answer at the terminal" {
		t.Errorf("timeout: %+v", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.ttyTimeout = time.Hour
	if d, _ := a.Approve(ctx, application.ApprovalRequest{Hook: domain.PrePush}); d.Approved || d.Rationale != "run cancelled at the approval prompt" {
		t.Errorf("cancelled: %+v", d)
	}
}

// The production opener points at the real controlling terminal and is wired
// into the approver `warden run` builds.
func TestNewTerminalApprover_UsesTheControllingTerminal(t *testing.T) {
	a := newTerminalApprover(strings.NewReader(""), io.Discard)
	if a.openTTY == nil || a.ttyTimeout != controllingTTYTimeout {
		t.Fatalf("approver not wired to the controlling terminal: %+v", a)
	}
	if f, err := openControllingTTY(); err == nil {
		_ = f.Close() // a developer's terminal; CI has none
	}
}
