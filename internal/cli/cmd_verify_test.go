package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// repoWithConfig builds a temp git repo containing .warden.yaml (or none when
// yaml is ""), chdirs into it, and returns the dir. git is required.
func repoWithConfig(t *testing.T, yaml string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, a := range [][]string{{"init"}, {"config", "user.email", "t@t.co"}, {"config", "user.name", "t"}, {"commit", "--allow-empty", "-m", "x"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	chdir(t, dir)
	return dir
}

// TestResolveTrustedKeys pins the precedence: an explicit --key wins; with no
// flag the committed roster supplies the keys; with neither, none are required.
func TestResolveTrustedKeys(t *testing.T) {
	t.Run("explicit --key wins over the roster", func(t *testing.T) {
		repoWithConfig(t, "trusted_keys:\n  - 0123456789abcdef\n")
		svc, err := newService(autoApprover{})
		if err != nil {
			t.Fatal(err)
		}
		keys, fromRoster := resolveTrustedKeys(svc, "aa,bb")
		if fromRoster || len(keys) != 2 {
			t.Errorf("explicit --key must win: keys=%v fromRoster=%v", keys, fromRoster)
		}
	})

	t.Run("no flag falls back to the roster", func(t *testing.T) {
		repoWithConfig(t, "trusted_keys:\n  - 0123456789abcdef\n")
		svc, err := newService(autoApprover{})
		if err != nil {
			t.Fatal(err)
		}
		keys, fromRoster := resolveTrustedKeys(svc, "")
		if !fromRoster || len(keys) != 1 || keys[0] != "0123456789abcdef" {
			t.Errorf("empty --key must use the roster: keys=%v fromRoster=%v", keys, fromRoster)
		}
	})

	t.Run("no flag and no roster requires nothing", func(t *testing.T) {
		repoWithConfig(t, "")
		svc, err := newService(autoApprover{})
		if err != nil {
			t.Fatal(err)
		}
		keys, fromRoster := resolveTrustedKeys(svc, "")
		if fromRoster || len(keys) != 0 {
			t.Errorf("no roster must require nothing: keys=%v fromRoster=%v", keys, fromRoster)
		}
	})
}

// TestParseRange pins the --range parser: it accepts a two-dot BASE..HEAD with
// both endpoints present, and rejects git's three-dot symmetric-difference form
// (a provenance gate must walk a definite ancestry range) and any spec with a
// missing endpoint or no separator.
func TestParseRange(t *testing.T) {
	cases := []struct {
		spec             string
		wantBase, wantHd string
		wantOK           bool
	}{
		{"origin/main..HEAD", "origin/main", "HEAD", true},
		{"abc123..def456", "abc123", "def456", true},
		{"main..feature/x", "main", "feature/x", true},
		{"origin/main...HEAD", "", "", false}, // three-dot rejected
		{"HEAD", "", "", false},               // no separator
		{"..HEAD", "", "", false},             // missing base
		{"main..", "", "", false},             // missing head
		{"", "", "", false},                   // empty
	}
	for _, tc := range cases {
		base, head, ok := parseRange(tc.spec)
		if ok != tc.wantOK || base != tc.wantBase || head != tc.wantHd {
			t.Errorf("parseRange(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.spec, base, head, ok, tc.wantBase, tc.wantHd, tc.wantOK)
		}
	}
}

func TestGateDepth(t *testing.T) {
	cases := []struct {
		opts service.RangeVerifyOptions
		want string
	}{
		{service.RangeVerifyOptions{}, "attested"},
		{service.RangeVerifyOptions{RequireSigned: true}, "signed"},
		{service.RangeVerifyOptions{TrustedKeys: []string{"fp"}}, "trusted-signed"},
		// Trust dominates the label even if RequireSigned is also set.
		{service.RangeVerifyOptions{RequireSigned: true, TrustedKeys: []string{"fp"}}, "trusted-signed"},
	}
	for _, tc := range cases {
		if got := gateDepth(tc.opts); got != tc.want {
			t.Errorf("gateDepth(%+v) = %q, want %q", tc.opts, got, tc.want)
		}
	}
}

func TestReasonHint(t *testing.T) {
	for _, r := range []domain.VerifyReason{
		domain.ReasonMissing, domain.ReasonBrokenChain, domain.ReasonUnsigned, domain.ReasonUntrusted,
	} {
		if h := reasonHint(r); h == "" || h == "ok" {
			t.Errorf("reasonHint(%q) should be a specific message, got %q", r, h)
		}
	}
	if h := reasonHint(domain.ReasonOK); h != "ok" {
		t.Errorf("reasonHint(ok) = %q, want ok", h)
	}
}

func TestPrintRange(t *testing.T) {
	opts := service.RangeVerifyOptions{}
	base, head := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"

	// All-pass renders a "verified N" line and no failure marks.
	pass := service.RangeVerifyResult{Base: base, Head: head, Commits: []domain.CommitVerdict{{SHA: "1111111111111111"}, {SHA: "2222222222222222"}}}
	var buf bytes.Buffer
	printRange(&buf, pass, opts)
	if out := buf.String(); !strings.Contains(out, "verified 2 commit") || strings.Contains(out, "✗") {
		t.Errorf("pass render wrong:\n%s", out)
	}

	// A failure renders a per-commit ✗ line with the reason and a FAILED verdict.
	fail := service.RangeVerifyResult{Base: base, Head: head, Commits: []domain.CommitVerdict{
		{SHA: "1111111111111111"},
		{SHA: "3333333333333333", Reason: domain.ReasonMissing},
	}}
	buf.Reset()
	printRange(&buf, fail, opts)
	out := buf.String()
	if !strings.Contains(out, "✗ 333333333333") || !strings.Contains(out, "no warden note") {
		t.Errorf("fail render missing per-commit reason:\n%s", out)
	}
	if !strings.Contains(out, "FAILED: 1 of 2") {
		t.Errorf("fail render missing verdict:\n%s", out)
	}
}

func TestPrintRangeJSON(t *testing.T) {
	res := service.RangeVerifyResult{Base: "abc", Head: "def", Commits: []domain.CommitVerdict{
		{SHA: "1111"},
		{SHA: "2222", Reason: domain.ReasonUntrusted},
	}}
	var out, errb bytes.Buffer
	if code := printRangeJSON(&out, &errb, res, service.RangeVerifyOptions{TrustedKeys: []string{"fp"}}); code != 0 {
		t.Fatalf("printRangeJSON code=%d err=%q", code, errb.String())
	}
	var got rangeExport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if got.OK || got.Failed != 1 || got.Total != 2 || got.Depth != "trusted-signed" {
		t.Errorf("unexpected export: %+v", got)
	}
	if got.Commits[1].Reason != domain.ReasonUntrusted {
		t.Errorf("per-commit reason not carried in JSON: %+v", got.Commits)
	}
}

// TestCmdVerifyRange_EndToEnd drives the whole --range path through cmdVerify
// against a real repo: commits with no notes fail the gate (exit 1), and a
// malformed range is a usage error (exit 2).
func TestCmdVerifyRange_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.email", "t@t.co")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "--no-verify", "-m", "c1")
	git("commit", "--allow-empty", "--no-verify", "-m", "c2")
	chdir(t, dir)

	// Un-noted commits → gate fails with a per-commit reason, exit 1.
	var out, errb bytes.Buffer
	if code := cmdVerify([]string{"--range", base + "..HEAD"}, &out, &errb); code != 1 {
		t.Fatalf("expected exit 1 for un-noted range, got %d (out=%q err=%q)", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "FAILED") {
		t.Errorf("expected a FAILED verdict, got:\n%s", out.String())
	}

	// --json still exits 1 but emits a parseable verdict.
	out.Reset()
	errb.Reset()
	if code := cmdVerify([]string{"--range", base + "..HEAD", "--json"}, &out, &errb); code != 1 {
		t.Fatalf("json range: expected exit 1, got %d", code)
	}
	var export rangeExport
	if err := json.Unmarshal(out.Bytes(), &export); err != nil {
		t.Fatalf("invalid --json output: %v\n%s", err, out.String())
	}
	if export.OK || export.Failed != 2 {
		t.Errorf("expected 2 failures in export, got %+v", export)
	}

	// A malformed range is a usage error, distinct from a gate failure.
	out.Reset()
	errb.Reset()
	if code := cmdVerify([]string{"--range", "HEAD"}, &out, &errb); code != 2 {
		t.Errorf("bad range: expected exit 2, got %d", code)
	}
}
