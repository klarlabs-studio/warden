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
