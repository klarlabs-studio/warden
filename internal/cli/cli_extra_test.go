package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// gitRepo creates a temp git repo with one commit, chdirs into it, and returns
// the directory. It skips the test when git is unavailable so the suite stays
// runnable on hosts without git.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t.co")
	git("config", "user.name", "t")
	git("commit", "--allow-empty", "-m", "init")
	chdir(t, dir)
	return dir
}

// writeConfig writes a .warden.yaml into dir.
func writeConfig(t *testing.T, dir, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHooks_EnableDisableAndErrors(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("init", "--hooks=pre-commit,pre-push"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}

	if code, out, errb := run("hooks", "enable", "pre-push"); code != 0 || !strings.Contains(out, "pre-push enabled") {
		t.Errorf("hooks enable: code=%d out=%q err=%q", code, out, errb)
	}
	if code, out, errb := run("hooks", "disable", "pre-commit"); code != 0 || !strings.Contains(out, "pre-commit disabled") {
		t.Errorf("hooks disable: code=%d out=%q err=%q", code, out, errb)
	}

	// Wrong argument count → usage, exit 2.
	if code, _, errb := run("hooks", "enable"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("hooks bad arity: code=%d err=%q", code, errb)
	}
	// Bad hook name → ParseHook error, exit 1.
	if code, _, errb := run("hooks", "enable", "not-a-hook"); code != 1 || !strings.Contains(errb, "unknown hook") {
		t.Errorf("hooks bad hook: code=%d err=%q", code, errb)
	}
	// Bad action → exit 2.
	if code, _, errb := run("hooks", "toggle", "pre-push"); code != 2 || !strings.Contains(errb, "unknown action") {
		t.Errorf("hooks bad action: code=%d err=%q", code, errb)
	}
}

func TestSteps_ListBuiltinAndCustom(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
  pre_push: [review, my-custom-step, lint]
`)
	code, out, errb := run("steps", "list")
	if code != 0 {
		t.Fatalf("steps list: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "lint") || !strings.Contains(out, "(built-in)") {
		t.Errorf("expected built-in labeling: %q", out)
	}
	if !strings.Contains(out, "my-custom-step") || !strings.Contains(out, "(custom)") {
		t.Errorf("expected custom labeling: %q", out)
	}

	// Bad subcommand → usage, exit 2.
	if code, _, errb := run("steps", "nope"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("steps bad subcommand: code=%d err=%q", code, errb)
	}
}

func TestDoctor_ReportsUnverifiedAndExits1(t *testing.T) {
	dir := gitRepo(t)
	if code, _, errb := run("init", "--hooks=pre-push"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}
	// A commit made after adoption with no warden note is unverified.
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "post-adoption change")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}

	code, out, _ := run("doctor")
	if code != 1 {
		t.Errorf("doctor with unverified commit should exit 1, got %d", code)
	}
	if !strings.Contains(out, "UNVERIFIED") || !strings.Contains(out, "unverified since adoption") {
		t.Errorf("doctor summary missing unverified reporting: %q", out)
	}
	if !strings.Contains(out, "since adoption") {
		t.Errorf("doctor header missing: %q", out)
	}
}

func TestDoctor_WithoutInitErrors(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("doctor"); code != 1 || !strings.Contains(errb, "warden") {
		t.Errorf("doctor without adoption: code=%d err=%q", code, errb)
	}
}

func TestDoctor_ShortAndTruncate(t *testing.T) {
	if got := short("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("short long: %q", got)
	}
	if got := short("abc"); got != "abc" {
		t.Errorf("short short: %q", got)
	}
	if got := truncate("hello", 40); got != "hello" {
		t.Errorf("truncate under: %q", got)
	}
	got := truncate("this is a rather long commit subject line", 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate over: %q (len %d)", got, len([]rune(got)))
	}
}

func TestPolicy_ExplainFormatsAgentAndAutoFix(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, `agent: auto
steps:
  pre_push: [review, lint]
commands:
  lint: "true"
rules:
  - match:
      paths: ["security/**"]
    then:
      agent:
        review: codex
      auto_fix:
        lint: 3
`)
	code, out, errb := run("policy", "explain", "--hook", "pre-push", "--paths", "security/token.go")
	if code != 0 {
		t.Fatalf("policy explain: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "agent=codex") {
		t.Errorf("missing agent annotation: %q", out)
	}
	if !strings.Contains(out, "auto_fix=3") {
		t.Errorf("missing auto_fix annotation: %q", out)
	}
	if !strings.Contains(out, "matched rules:") {
		t.Errorf("missing matched rules line: %q", out)
	}
}

func TestPolicy_ExplainNoRulesAndErrors(t *testing.T) {
	gitRepo(t)
	// No matching rules → "(none)".
	if code, out, _ := run("policy", "explain", "--hook", "pre-commit"); code != 0 || !strings.Contains(out, "(none)") {
		t.Errorf("policy explain none: code=%d out=%q", code, out)
	}
	// Missing explain subcommand → usage, exit 2.
	if code, _, errb := run("policy"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("policy no subcommand: code=%d err=%q", code, errb)
	}
	if code, _, errb := run("policy", "nope"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("policy bad subcommand: code=%d err=%q", code, errb)
	}
	// Bad hook → ParseHook error, exit 1.
	if code, _, errb := run("policy", "explain", "--hook", "bogus"); code != 1 || !strings.Contains(errb, "unknown hook") {
		t.Errorf("policy bad hook: code=%d err=%q", code, errb)
	}
}

func TestAxi_Surfaces(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
  pre_push: [review, lint]
commands:
  lint: "true"
`)

	// policy-explain emits TOON with the resolved shape.
	code, out, errb := run("axi", "policy-explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("axi policy-explain: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"hook", "risk", "require_approval", "steps", "pre-push"} {
		if !strings.Contains(out, want) {
			t.Errorf("axi policy-explain output missing %q: %q", want, out)
		}
	}

	// steps emits both hook lists.
	code, out, errb = run("axi", "steps")
	if code != 0 {
		t.Fatalf("axi steps: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "pre_commit") || !strings.Contains(out, "pre_push") {
		t.Errorf("axi steps output shape: %q", out)
	}

	// run-trigger is refused by default: it executes repo-authored shell on the
	// auto-approved path, so without explicit trust it must not run.
	if code, _, errb := run("axi", "run-trigger", "--hook", "pre-commit"); code != 1 || !strings.Contains(errb, "run_trigger refused") {
		t.Errorf("axi run-trigger untrusted should refuse: code=%d err=%q", code, errb)
	}

	// With --trust it runs and reports the outcome.
	code, out, errb = run("axi", "run-trigger", "--hook", "pre-commit", "--trust")
	if code != 0 {
		t.Fatalf("axi run-trigger --trust: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "outcome") {
		t.Errorf("axi run-trigger output shape: %q", out)
	}

	// The env-var opt-in is equivalent to --trust for repos the operator trusts.
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "1")
	if code, out, errb := run("axi", "run-trigger", "--hook", "pre-commit"); code != 0 || !strings.Contains(out, "outcome") {
		t.Errorf("axi run-trigger with env opt-in: code=%d out=%q err=%q", code, out, errb)
	}
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "")

	// Unknown verb → exit 2.
	if code, _, errb := run("axi", "bogus"); code != 2 || !strings.Contains(errb, "unknown verb") {
		t.Errorf("axi unknown verb: code=%d err=%q", code, errb)
	}
	// No verb → usage, exit 2.
	if code, _, errb := run("axi"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("axi no verb: code=%d err=%q", code, errb)
	}
}

func TestAxi_EmitTOONAndStringHelpers(t *testing.T) {
	var out, errb bytes.Buffer
	if code := emitTOON(&out, &errb, map[string]any{"k": "v"}); code != 0 {
		t.Fatalf("emitTOON: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "k") || !strings.Contains(out.String(), "v") {
		t.Errorf("emitTOON output: %q", out.String())
	}

	if got := stepStrings([]domain.StepName{"review", "lint"}); len(got) != 2 || got[0] != "review" {
		t.Errorf("stepStrings: %v", got)
	}
	if got := anyStrings([]string{"a", "b"}); len(got) != 2 || got[1] != "b" {
		t.Errorf("anyStrings: %v", got)
	}
	if got := anyStrings(nil); len(got) != 0 {
		t.Errorf("anyStrings nil: %v", got)
	}
}

func TestMCPRunTrusted(t *testing.T) {
	// Explicit trust (the axi --trust flag) permits regardless of env.
	t.Setenv("WARDEN_MCP_ALLOW_RUN", "")
	if !mcpRunTrusted(true) {
		t.Error("explicit trust should permit")
	}
	// Without explicit trust and no env, refuse.
	if mcpRunTrusted(false) {
		t.Error("no trust signal should refuse")
	}
	// Truthy env spellings opt in; anything else stays off.
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		t.Setenv("WARDEN_MCP_ALLOW_RUN", v)
		if !mcpRunTrusted(false) {
			t.Errorf("env %q should permit", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "bogus"} {
		t.Setenv("WARDEN_MCP_ALLOW_RUN", v)
		if mcpRunTrusted(false) {
			t.Errorf("env %q should refuse", v)
		}
	}
}

func TestErrUntrustedMCPRun_IsActionable(t *testing.T) {
	msg := errUntrustedMCPRun().Error()
	for _, want := range []string{"run_trigger refused", "WARDEN_MCP_ALLOW_RUN", "--trust"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message missing %q: %q", want, msg)
		}
	}
}

func TestInit_DefaultAndBadHooks(t *testing.T) {
	gitRepo(t)
	// Default init installs both hooks.
	code, out, errb := run("init")
	if code != 0 {
		t.Fatalf("init default: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "pre-commit") || !strings.Contains(out, "pre-push") {
		t.Errorf("init default should install both hooks: %q", out)
	}
	if !strings.Contains(out, "adoption point recorded") {
		t.Errorf("init missing adoption note: %q", out)
	}

	// Bad --hooks value → ParseHook error, exit 1.
	if code, _, errb := run("init", "--hooks=bogus"); code != 1 || !strings.Contains(errb, "unknown hook") {
		t.Errorf("init bad hooks: code=%d err=%q", code, errb)
	}
	// Unknown flag → flag parse error, exit 2.
	if code, _, _ := run("init", "--nope"); code != 2 {
		t.Errorf("init bad flag: code=%d", code)
	}
}

func TestRun_PreCommitPassesAndFindingsHelper(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "true"
`)
	code, out, errb := run("run", "pre-commit")
	if code != 0 {
		t.Fatalf("run pre-commit: code=%d out=%q err=%q", code, out, errb)
	}
	if !strings.Contains(out, "pre-commit passed") {
		t.Errorf("run pre-commit output: %q", out)
	}

	// Bad hook → exit 1.
	if code, _, errb := run("run", "bogus"); code != 1 || !strings.Contains(errb, "unknown hook") {
		t.Errorf("run bad hook: code=%d err=%q", code, errb)
	}
	// No hook → usage, exit 2.
	if code, _, errb := run("run"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("run no hook: code=%d err=%q", code, errb)
	}
}

func TestRun_PrintFindings(t *testing.T) {
	var buf bytes.Buffer
	printFindings(&buf, []domain.Finding{
		{Severity: domain.SeverityHigh, File: "a.go", Line: 12, Message: "boom"},
		{Severity: domain.SeverityLow, File: "b.go", Message: "no line"},
	})
	out := buf.String()
	if !strings.Contains(out, "[high] a.go:12 boom") {
		t.Errorf("finding with line: %q", out)
	}
	if !strings.Contains(out, "[low] b.go no line") {
		t.Errorf("finding without line: %q", out)
	}
}

func TestApprover_NonTTYDeclines(t *testing.T) {
	// Constructor coverage; its tty value is environment-dependent.
	_ = newTerminalApprover(strings.NewReader(""), &bytes.Buffer{})

	req := application.ApprovalRequest{
		Hook:   domain.PrePush,
		Branch: "main",
		Risk:   domain.RiskHigh,
		Findings: []domain.Finding{
			{Severity: domain.SeverityHigh, File: "x.go", Line: 3, Message: "risky"},
			{Severity: domain.SeverityLow, File: "y.go", Message: "note"},
		},
	}

	// Non-tty stream declines with a "no tty" rationale.
	var out bytes.Buffer
	a := terminalApprover{in: strings.NewReader(""), out: &out, tty: false}
	dec, err := a.Approve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Approved {
		t.Error("non-tty stream must decline")
	}
	if dec.Rationale != "no tty" {
		t.Errorf("rationale = %q, want %q", dec.Rationale, "no tty")
	}
	if !strings.Contains(out.String(), "needs approval") || !strings.Contains(out.String(), "non-interactive") {
		t.Errorf("approver output: %q", out.String())
	}
	if !strings.Contains(out.String(), "x.go:3") || !strings.Contains(out.String(), "y.go") {
		t.Errorf("approver should list findings: %q", out.String())
	}
}

func TestApprover_TTYYesNo(t *testing.T) {
	req := application.ApprovalRequest{Hook: domain.PrePush, Branch: "main", Risk: domain.RiskLow}

	var out bytes.Buffer
	yes := terminalApprover{in: strings.NewReader("y\n"), out: &out, tty: true}
	if dec, _ := yes.Approve(context.Background(), req); !dec.Approved {
		t.Error("y should approve")
	}

	no := terminalApprover{in: strings.NewReader("n\n"), out: &bytes.Buffer{}, tty: true}
	if dec, _ := no.Approve(context.Background(), req); dec.Approved {
		t.Error("n should decline")
	}
}

func TestStatus_ReportsGateState(t *testing.T) {
	gitRepo(t)
	// Before init: reports "not initialized".
	code, out, errb := run("status")
	if code != 0 {
		t.Fatalf("status: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "hooks:") || !strings.Contains(out, "not initialized") {
		t.Errorf("status before init: %q", out)
	}

	// After init: reports armed hooks and adoption point.
	if code, _, errb := run("init"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}
	code, out, _ = run("status")
	if code != 0 {
		t.Fatalf("status after init: code=%d", code)
	}
	if !strings.Contains(out, "armed") || !strings.Contains(out, "adoption point:") {
		t.Errorf("status after init: %q", out)
	}
	if !strings.Contains(out, "steps:") {
		t.Errorf("status should list steps: %q", out)
	}
}

func TestMCP_UsageError(t *testing.T) {
	gitRepo(t)
	if code, _, errb := run("mcp"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("mcp no subcommand: code=%d err=%q", code, errb)
	}
	if code, _, errb := run("mcp", "bogus"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("mcp bad subcommand: code=%d err=%q", code, errb)
	}
}

func TestRun_PrePushExitAndAutoApprover(t *testing.T) {
	// runPrePushExit always returns 1 and echoes the run message.
	var out bytes.Buffer
	if code := runPrePushExit(application.RunResult{Message: "pushed"}, &out); code != 1 {
		t.Errorf("runPrePushExit code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "pushed") {
		t.Errorf("runPrePushExit output: %q", out.String())
	}

	// autoApprover approves unconditionally.
	dec, err := autoApprover{}.Approve(context.Background(), application.ApprovalRequest{})
	if err != nil || !dec.Approved || dec.Principal != "warden-auto" {
		t.Errorf("autoApprover: %+v err=%v", dec, err)
	}
}

func TestRun_PreCommitFailAborts(t *testing.T) {
	dir := gitRepo(t)
	// A failing lint command makes the pre-commit run fail → exit 1.
	writeConfig(t, dir, `steps:
  pre_commit: [lint]
commands:
  lint: "exit 3"
`)
	code, _, errb := run("run", "pre-commit")
	if code != 1 {
		t.Errorf("failing pre-commit should exit 1, got %d (err=%q)", code, errb)
	}
	if !strings.Contains(errb, "warden:") {
		t.Errorf("expected failure message on stderr: %q", errb)
	}
}

func TestCommon_SplitListAndParseHooks(t *testing.T) {
	if got := splitList(" a , ,b, c "); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitList: %v", got)
	}
	if got := splitList(""); got != nil {
		t.Errorf("splitList empty: %v", got)
	}

	// Empty defaults to both hooks.
	hooks, err := parseHooksFlag("")
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != len(domain.AllHooks) {
		t.Errorf("default hooks = %v", hooks)
	}

	hooks, err = parseHooksFlag("pre-push")
	if err != nil || len(hooks) != 1 || hooks[0] != domain.PrePush {
		t.Errorf("single hook: %v %v", hooks, err)
	}

	if _, err := parseHooksFlag("pre-commit,bogus"); err == nil {
		t.Error("bad hook value should error")
	}
}
