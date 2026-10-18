package steps

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestExpandTemplate(t *testing.T) {
	if got := expandTemplate("", "p", domain.StepReview, "/r"); got != "" {
		t.Errorf("empty template should stay empty, got %q", got)
	}
	got := expandTemplate("claude -p {prompt} --step {step} --cwd {repo}", "hi", domain.StepReview, "/wt")
	// {step} is not quoted; {prompt}/{repo} are single-quoted.
	if !strings.Contains(got, "--step review") {
		t.Errorf("{step} not substituted: %q", got)
	}
	if !strings.Contains(got, "'hi'") || !strings.Contains(got, "'/wt'") {
		t.Errorf("{prompt}/{repo} not quoted-substituted: %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	// A prompt with a single quote must not break out of the quoting.
	q := shellQuote("it's a test")
	if q != `'it'\''s a test'` {
		t.Errorf("shellQuote escaping wrong: %s", q)
	}
}

func TestAgentStep_NoCommandSkips(t *testing.T) {
	ctx := context.Background()
	step := NewAgentStep(domain.StepReview, "review this")

	cases := []application.StepContext{
		{WorktreeDir: t.TempDir()},                                    // no agent
		{WorktreeDir: t.TempDir(), Agent: "claude"},                   // agent but no command
		{WorktreeDir: t.TempDir(), Agent: "", AgentCommand: "echo x"}, // command but no agent
	}
	for i, sc := range cases {
		res, err := step.Run(ctx, sc)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if res.Status != domain.StepPass || !strings.Contains(res.Summary, "skipped") {
			t.Errorf("case %d: expected advisory skip, got %+v", i, res)
		}
		if len(res.Findings) != 0 {
			t.Errorf("case %d: skipped step should have no findings", i)
		}
		// The pass is a verdict; the marker is what stops every surface
		// reporting the review as one that ran.
		if res.Outcome != domain.StepSkipped || res.ExecutionOutcome() != domain.StepSkipped {
			t.Errorf("case %d: a skip must be marked StepSkipped, got %q", i, res.Outcome)
		}
	}
}

func TestAgentStep_SkipReasonNamesWhatIsMissing(t *testing.T) {
	step := NewAgentStep(domain.StepReview, "review this")
	for _, tc := range []struct {
		sc   application.StepContext
		want string
	}{
		{application.StepContext{WorktreeDir: t.TempDir()}, "no agent configured"},
		{application.StepContext{WorktreeDir: t.TempDir(), Agent: "aider"}, "no command configured for agent aider"},
	} {
		res, err := step.Run(context.Background(), tc.sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.OutcomeReason != tc.want {
			t.Errorf("reason = %q, want %q", res.OutcomeReason, tc.want)
		}
		if want := "review: skipped (" + tc.want + ")"; res.Summary != want {
			t.Errorf("summary = %q, want %q", res.Summary, want)
		}
	}
}

func TestAgentStep_ConfiguredCommandPasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command assumes a POSIX shell")
	}
	step := NewAgentStep(domain.StepIntent, "summarize")
	res, err := step.Run(context.Background(), application.StepContext{
		WorktreeDir:  t.TempDir(),
		Agent:        "faux",
		AgentCommand: "echo reviewed {prompt}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass", res.Status)
	}
	if !strings.Contains(res.Summary, "faux") || !strings.Contains(res.Summary, "reviewed") {
		t.Errorf("summary should carry agent + output: %q", res.Summary)
	}
}

func TestAgentStep_FailingCommandReportsFinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command assumes a POSIX shell")
	}
	step := NewAgentStep(domain.StepReview, "review")
	res, err := step.Run(context.Background(), application.StepContext{
		WorktreeDir:  t.TempDir(),
		Agent:        "faux",
		AgentCommand: "echo boom >&2; exit 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Errorf("status = %s, want fail", res.Status)
	}
	if len(res.Findings) != 1 || res.Findings[0].Severity != domain.SeverityMedium {
		t.Errorf("expected one medium finding, got %+v", res.Findings)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"single line", "single line"},
		{"first\nsecond\nthird", "first"},
		{"  padded first\nsecond  ", "padded first"},
		{"\n\nleading blank", "leading blank"},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgentStepName(t *testing.T) {
	if NewAgentStep(domain.StepDocument, "x").Name() != domain.StepDocument {
		t.Error("Name mismatch")
	}
}

// Under a profile that requires agent steps to execute, a missing agent or
// command is a failure with a config blocker, naming the profile and the fix,
// and never runs anything.
func TestAgentStep_RequiredExecutionFailsWithConfigBlocker(t *testing.T) {
	step := NewAgentStep(domain.StepReview, "review")
	boundary := domain.AgentBoundary{Profile: "standard@1", RequireExecution: true}
	for _, tc := range []struct {
		sc   application.StepContext
		want string
	}{
		{application.StepContext{WorktreeDir: t.TempDir(), AgentBoundary: boundary}, "set `agent: claude` or `agent: codex`"},
		{application.StepContext{WorktreeDir: t.TempDir(), Agent: "faux", AgentBoundary: boundary}, "add agent_commands.faux"},
	} {
		res, err := step.Run(context.Background(), tc.sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepFail || res.Blocker != domain.BlockerConfig || res.ExecutionOutcome() != domain.StepSkipped {
			t.Fatalf("want a config-blocked failure that did not execute, got %+v", res)
		}
		if !strings.Contains(res.Findings[0].Message, "profile standard@1 requires review to run") || !strings.Contains(res.Findings[0].Message, tc.want) {
			t.Errorf("message must name the profile and the fix %q: %s", tc.want, res.Findings[0].Message)
		}
	}
}

// Under strict the agent sees only the allowlist: a token in warden's own
// environment does not reach it, while its preset's documented key does.
func TestAgentStep_EnvAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command assumes a POSIX shell")
	}
	t.Setenv("FAKE_TOKEN", "leak")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	cmd := `test -z "$FAKE_TOKEN" && test "$ANTHROPIC_API_KEY" = key`
	step := NewAgentStep(domain.StepReview, "review")
	run := func(b domain.AgentBoundary) domain.StepResult {
		res, err := step.Run(context.Background(), application.StepContext{WorktreeDir: t.TempDir(), Agent: "claude", AgentCommand: cmd, AgentBoundary: b})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run(domain.AgentBoundary{Profile: "strict@1", EnvAllowlist: true, Isolate: true}); res.Status != domain.StepPass || res.AgentBoundary == nil || !res.AgentBoundary.EnvAllowlisted {
		t.Errorf("strict: the agent must see only the allowlist: %+v", res)
	}
	if res := run(domain.AgentBoundary{}); res.Status != domain.StepFail || res.AgentBoundary != nil {
		t.Errorf("without a profile the agent keeps the full environment, as before: %+v", res)
	}
}

// A custom agent under an allowlisted environment is refused without running.
func TestAgentStep_EnvAllowlistRefusesCustomAgent(t *testing.T) {
	step := NewAgentStep(domain.StepReview, "review")
	res, err := step.Run(context.Background(), application.StepContext{
		WorktreeDir: t.TempDir(), Agent: "faux", AgentCommand: "touch ran",
		AgentBoundary: domain.AgentBoundary{Profile: "strict@1", EnvAllowlist: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail || res.Blocker != domain.BlockerConfig || !strings.Contains(res.Summary, "not a bundled preset") {
		t.Fatalf("custom agent under strict: %+v", res)
	}
}
