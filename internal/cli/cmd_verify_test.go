package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
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
	t.Chdir(dir)
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
	base, head := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"

	// All-pass renders a "verified N" line and no failure marks.
	pass := service.RangeVerifyResult{Base: base, Head: head, Commits: []domain.CommitVerdict{{SHA: "1111111111111111"}, {SHA: "2222222222222222"}}}
	var buf bytes.Buffer
	printRange(&buf, pass)
	if out := buf.String(); !strings.Contains(out, "verified 2 commit") || strings.Contains(out, "✗") {
		t.Errorf("pass render wrong:\n%s", out)
	}

	// A failure renders a per-commit ✗ line with the reason and a FAILED verdict.
	fail := service.RangeVerifyResult{Base: base, Head: head, Commits: []domain.CommitVerdict{
		{SHA: "1111111111111111"},
		{SHA: "3333333333333333", Reason: domain.ReasonMissing},
	}}
	buf.Reset()
	printRange(&buf, fail)
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
	}, Effective: service.RangeVerifyOptions{TrustedKeys: []string{"fp"}}}
	var out, errb bytes.Buffer
	if code := printRangeJSON(&out, &errb, res); code != 0 {
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
	t.Chdir(dir)

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

// TestCmdVerifyRange_RosterFromBase is the Finding-1 end-to-end guard: a range
// gate reads its trusted_keys roster from the BASE ref, so a PR that adds its own
// signer to .warden.yaml AND signs its commits with that key still FAILS — the
// widened roster on the head is never consulted. Had the gate read the roster
// from the working tree (the head), this would pass, which is the vulnerability.
func TestCmdVerifyRange_RosterFromBase(t *testing.T) {
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

	// The trusted signer pinned by the base, and the attacker's key.
	trustedPub, _, _ := ed25519.GenerateKey(nil)
	trustedFP := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(trustedPub))
	attackerPub, attackerPriv, _ := ed25519.GenerateKey(nil)

	writeRoster := func(keys ...string) {
		var body strings.Builder
		body.WriteString("trusted_keys:\n")
		for _, k := range keys {
			body.WriteString("  - " + k + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(body.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".warden.yaml")
	}

	// BASE: pins only the trusted signer.
	writeRoster(trustedFP)
	git("commit", "--no-verify", "-m", "base roster")
	base := git("rev-parse", "HEAD")

	// HEAD (the attack): the PR both widens the roster to include the attacker key
	// and is authored under it.
	writeRoster(trustedFP, domain.KeyFingerprint(base64.StdEncoding.EncodeToString(attackerPub)))
	git("commit", "--no-verify", "-m", "widen roster + attacker change")
	head := git("rev-parse", "HEAD")

	t.Chdir(dir)
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	// The attacker self-signs a valid, commit-bound note for the head commit.
	rec := domain.RunRecord{
		RunID: "atk", CommitSHA: head, StepsRun: []domain.StepName{"lint"},
		EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}},
		PublicKey: base64.StdEncoding.EncodeToString(attackerPub),
	}
	p, _ := rec.SigningPayload()
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(attackerPriv, p))
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := cmdVerify([]string{"--range", base + ".." + head}, &out, &errb)
	if code != 1 {
		t.Fatalf("gate must FAIL when the head's own roster is the only thing trusting the signer; got exit %d\nout=%s", code, out.String())
	}
	// The roster came from the base ref, and the reason is untrusted (not merely
	// missing/unsigned) — the note verifies, just not against a base-trusted key.
	if !strings.Contains(out.String(), base+":.warden.yaml") {
		t.Errorf("expected the summary to name the base roster source, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "outside the trusted set") {
		t.Errorf("expected an untrusted-signer reason, got:\n%s", out.String())
	}
}

func TestVerifyContainerRequirements(t *testing.T) {
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "sha256:" + strings.Repeat("a", 64)
	record := domain.RunRecord{RunID: "container-run", CommitSHA: head, StepsRun: []domain.StepName{domain.StepTest}, EvidenceChainRoot: "root", Evidence: []domain.EvidenceEntry{{Hash: "root"}}, PublicKey: base64.StdEncoding.EncodeToString(pub), Environments: map[domain.StepName]domain.ExecutionEnvironment{domain.StepTest: {Runtime: "podman", Image: "checks:dev", ImageID: id, Platform: "linux/arm64", Network: "none"}}}
	payload, err := record.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"trusted", []string{"--key", record.SignerFingerprint(), "--container-steps", "test", "--container-image-id", id}, 0},
		{"no trust", []string{"--container-steps", "test"}, 1},
		{"missing step", []string{"--key", record.SignerFingerprint(), "--container-steps", "lint"}, 1},
		{"wrong image", []string{"--key", record.SignerFingerprint(), "--container-steps", "test", "--container-image-id", "sha256:" + strings.Repeat("b", 64)}, 1},
		{"invalid step", []string{"--container-steps", "../test"}, 2},
		{"empty list", []string{"--container-steps", ","}, 2},
		{"explicit empty requirement", []string{"--container-steps", ""}, 2},
		{"image without steps", []string{"--container-image-id", id}, 2},
		{"invalid image", []string{"--container-steps", "test", "--container-image-id", "tag"}, 2},
		{"range unsupported", []string{"--range", "HEAD~1..HEAD", "--container-steps", "test"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cmdVerify(tc.args, &out, &errb); code != tc.want {
				t.Fatalf("exit %d want %d: %s %s", code, tc.want, out.String(), errb.String())
			}
		})
	}
}

// Live mode under a container configuration runs container shell checks;
// with none configured there is nothing to watch, and it says so before
// taking the worktree's lock.
func TestWatchLiveContainerNeedsShellChecks(t *testing.T) {
	dir := repoWithConfig(t, "execution:\n  image: checks:dev\n")
	var out, errb bytes.Buffer
	if code := cmdWatch(nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no read-only container shell checks") {
		t.Fatalf("watch: %d %s %s", code, out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "warden", "feedback")); !os.IsNotExist(err) {
		t.Fatalf("a refused live watch touched the feedback directory: %v", err)
	}
}

func TestVerifyMinProfile(t *testing.T) {
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := domain.ParseComplianceProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.NewProfileRef(standard)
	executed := domain.StepObservation{Outcome: domain.StepExecuted}
	record := domain.RunRecord{
		RunID: "profiled-run", CommitSHA: head,
		StepsRun:          []domain.StepName{domain.StepCredentials, domain.StepLint, domain.StepTest},
		EvidenceChainRoot: "root", Evidence: []domain.EvidenceEntry{{Hash: "root"}},
		PublicKey:    base64.StdEncoding.EncodeToString(pub),
		Profile:      &ref,
		StepOutcomes: map[domain.StepName]domain.StepObservation{domain.StepCredentials: executed, domain.StepLint: executed, domain.StepTest: executed},
		Approval:     &domain.ApprovalRef{Channel: domain.ApprovalNone},
	}
	payload, err := record.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	fp := record.SignerFingerprint()
	writeReq := func(body string) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "requirements.json")
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	v2Standard := writeReq(`{"version":2,"profile":{"min":"standard"}}`)
	v2Strict := writeReq(`{"version":2,"profile":{"min":"strict"}}`)
	for _, tc := range []struct {
		name    string
		args    []string
		want    int
		wantOut string
	}{
		{"meets standard", []string{"--key", fp, "--min-profile", "standard"}, 0, "conforms to standard@1; checked: trusted, commit-bound local provenance"},
		{"meets permissive", []string{"--key", fp, "--min-profile", "permissive"}, 0, "conforms to permissive@1"},
		{"below strict", []string{"--key", fp, "--min-profile", "strict"}, 1, "gated under standard@1"},
		{"untrusted key", []string{"--key", strings.Repeat("ab", 8), "--min-profile", "standard"}, 1, "unverified"},
		{"no pinned key", []string{"--min-profile", "standard"}, 2, "needs a pinned --key"},
		{"blank pinned key", []string{"--key", " , ", "--min-profile", "standard"}, 2, "needs a pinned --key"},
		{"unknown level", []string{"--key", fp, "--min-profile", "paranoid"}, 2, "invalid --min-profile"},
		{"unknown version", []string{"--key", fp, "--min-profile", "strict@4"}, 2, "invalid --min-profile"},
		{"explicit empty", []string{"--key", fp, "--min-profile", ""}, 2, "invalid --min-profile"},
		{"v2 file meets standard", []string{"--key", fp, "--requirements", v2Standard}, 0, "conforms to standard@1"},
		{"v2 file below strict", []string{"--key", fp, "--requirements", v2Strict}, 1, "profile strict@1"},
		{"v2 file plus flag", []string{"--key", fp, "--requirements", v2Standard, "--min-profile", "strict"}, 1, "profile strict@1"},
		{"v2 file needs a pinned key", []string{"--requirements", v2Standard}, 2, "needs a pinned --key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := cmdVerify(tc.args, &out, &errb)
			if code != tc.want {
				t.Fatalf("exit %d want %d: %s %s", code, tc.want, out.String(), errb.String())
			}
			if all := out.String() + errb.String(); !strings.Contains(all, tc.wantOut) {
				t.Fatalf("output lacks %q:\n%s", tc.wantOut, all)
			}
		})
	}
}

// TestCmdVerifyRange_MinProfile drives `verify --range --min-profile` over a
// real repository whose only noted commit was gated under standard: it
// conforms to standard, not to strict, and without any key it cannot conform
// to anything.
func TestCmdVerifyRange_MinProfile(t *testing.T) {
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", "gated")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := domain.ParseComplianceProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.NewProfileRef(standard)
	executed := domain.StepObservation{Outcome: domain.StepExecuted}
	record := domain.RunRecord{
		RunID: "profiled-run", CommitSHA: head,
		StepsRun:          []domain.StepName{domain.StepCredentials, domain.StepLint, domain.StepTest},
		EvidenceChainRoot: "root", Evidence: []domain.EvidenceEntry{{Hash: "root"}},
		PublicKey:    base64.StdEncoding.EncodeToString(pub),
		Profile:      &ref,
		StepOutcomes: map[domain.StepName]domain.StepObservation{domain.StepCredentials: executed, domain.StepLint: executed, domain.StepTest: executed},
		Approval:     &domain.ApprovalRef{Channel: domain.ApprovalNone},
	}
	payload, err := record.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	fp := record.SignerFingerprint()
	rng := base + "..HEAD"
	for _, tc := range []struct {
		name     string
		args     []string
		want     int
		wantOut  []string
		denyOuts []string
	}{
		{"meets standard", []string{"--range", rng, "--key", fp, "--min-profile", "standard"}, 0,
			[]string{"requiring compliance profile standard@1 (from --min-profile)", "every noted commit conforms to standard@1"}, nil},
		{"below strict", []string{"--range", rng, "--key", fp, "--min-profile", "strict"}, 1,
			[]string{"does not conform to a required compliance profile", "profile strict@1: recorded profile ranks at least strict@1: the note was gated under standard@1", "conforming to strict@1"}, nil},
		{"no key and no base roster", []string{"--range", rng, "--min-profile", "standard"}, 1,
			[]string{"a profile needs a pinned --key or a trusted_keys roster at the base ref"}, nil},
		{"no profile anywhere", []string{"--range", rng, "--key", fp}, 0,
			[]string{"verified 1 commit"}, []string{"profile", "conforms"}},
		{"unknown level", []string{"--range", rng, "--key", fp, "--min-profile", "paranoid"}, 2,
			[]string{"invalid --min-profile"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := cmdVerify(tc.args, &out, &errb)
			all := out.String() + errb.String()
			if code != tc.want {
				t.Fatalf("exit %d want %d: %s", code, tc.want, all)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(all, w) {
					t.Errorf("output lacks %q:\n%s", w, all)
				}
			}
			for _, d := range tc.denyOuts {
				if strings.Contains(all, d) {
					t.Errorf("output must not mention %q when no profile applied:\n%s", d, all)
				}
			}
		})
	}

	t.Run("json names the profile and the failed clause", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := cmdVerify([]string{"--range", rng, "--key", fp, "--min-profile", "strict", "--json"}, &out, &errb); code != 1 {
			t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
		}
		var got rangeExport
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, out.String())
		}
		if len(got.Profiles) != 1 || got.Profiles[0] != (rangeProfileExport{Profile: "strict@1", FromFlag: true}) {
			t.Errorf("profiles = %+v", got.Profiles)
		}
		if len(got.Commits) != 1 || got.Commits[0].Reason != domain.ReasonProfile || len(got.Commits[0].ProfileFailures) == 0 {
			t.Errorf("commits = %+v", got.Commits)
		}
	})
	t.Run("json without a profile has no profiles key", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := cmdVerify([]string{"--range", rng, "--key", fp, "--json"}, &out, &errb); code != 0 {
			t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
		}
		if strings.Contains(out.String(), "profile") {
			t.Errorf("an unprofiled range must not emit profile fields:\n%s", out.String())
		}
	})
}

func TestPrintRangeProfile(t *testing.T) {
	strict, err := domain.ParseComplianceProfile("strict")
	if err != nil {
		t.Fatal(err)
	}
	profiles := []service.RangeProfile{{Profile: strict, FromBase: true}}
	pass := service.RangeVerifyResult{Base: "aaaaaaaaaaaa", Head: "bbbbbbbbbbbb", Profiles: profiles, Commits: []domain.CommitVerdict{
		{SHA: "1111111111111111"},
		{SHA: "2222222222222222", ForgeSigner: "fp", ProfileUnchecked: true},
	}}
	var buf bytes.Buffer
	printRange(&buf, pass)
	for _, want := range []string{"every noted commit conforms to strict@1", "1 forge-accepted commit(s) NOT profile-checked"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("pass render lacks %q:\n%s", want, buf.String())
		}
	}
	fail := service.RangeVerifyResult{Base: "aaaaaaaaaaaa", Head: "bbbbbbbbbbbb", Profiles: profiles, Commits: []domain.CommitVerdict{
		{SHA: "3333333333333333", Reason: domain.ReasonProfile, ProfileFailures: []string{"profile strict@1: x: y"}},
	}}
	buf.Reset()
	printRange(&buf, fail)
	for _, want := range []string{"✗ 333333333333", "      profile strict@1: x: y", "lack attested provenance conforming to strict@1"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("fail render lacks %q:\n%s", want, buf.String())
		}
	}
	if got := profileSource(service.RangeProfile{FromBase: true, FromFlag: true}, "main"); got != "from main:.warden.yaml and --min-profile" {
		t.Errorf("profileSource = %q", got)
	}
}

func TestPrintConformanceNamesDeferredClauses(t *testing.T) {
	var out bytes.Buffer
	printConformance(&out, []service.ConformanceReport{{Min: "strict@1", Checked: []string{"a", "b"}, Deferred: []string{"c"}}})
	want := "  conforms to strict@1; checked: a; b\n  not checked by this release: c\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

func TestLoadExecutionRequirementsVersion2(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"version":2,"profile":{"min":"strict"}}`, true},
		{`{"version":2,"profile":{"min":"strict","max":"strict"}}`, false},
		{`{"version":2,"profile":{"min":"strict"},"profile":{"min":"permissive"}}`, false},
		{`{"version":1,"profile":{"min":"strict"},"steps":{}}`, false},
		{`{"version":2}`, false},
	} {
		file := filepath.Join(t.TempDir(), "requirements.json")
		if err := os.WriteFile(file, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadExecutionRequirements(file); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v want ok=%v", tc.body, err, tc.ok)
		}
	}
}

func TestLoadExecutionRequirementsObservedLimits(t *testing.T) {
	digest := domain.DigestBytes([]byte("fixture"))
	step := domain.StepRequirements{Command: "true", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none", ObservedLimits: &domain.ExecutionLimits{Memory: "512MiB", CPUs: "1", Pids: 128}}
	for _, version := range []int{1, 2, 3} {
		data, err := json.Marshal(domain.ExecutionRequirements{Version: version, Steps: map[domain.StepName]domain.StepRequirements{domain.StepTest: step}})
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "requirements.json")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		req, err := loadExecutionRequirements(file)
		if (err == nil) != (version == 3) {
			t.Fatalf("version %d: %v", version, err)
		}
		if version == 3 && (req.Steps[domain.StepTest].ObservedLimits == nil || *req.Steps[domain.StepTest].ObservedLimits != *step.ObservedLimits) {
			t.Fatal("bounds lost while loading requirements")
		}
	}
}
