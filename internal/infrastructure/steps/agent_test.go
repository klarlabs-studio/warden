package steps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestResolveAgentBinary(t *testing.T) {
	t.Run("empty is an advisory skip", func(t *testing.T) {
		if got := resolveAgentBinary(""); got != "" {
			t.Errorf("resolveAgentBinary(\"\") = %q, want \"\"", got)
		}
	})

	t.Run("auto is an advisory skip", func(t *testing.T) {
		if got := resolveAgentBinary("auto"); got != "" {
			t.Errorf("resolveAgentBinary(\"auto\") = %q, want \"\"", got)
		}
	})

	t.Run("explicit name not on PATH yields empty", func(t *testing.T) {
		if got := resolveAgentBinary("warden-nonexistent-agent-xyz"); got != "" {
			t.Errorf("resolveAgentBinary(missing) = %q, want \"\"", got)
		}
	})

	t.Run("explicit name on PATH resolves to an absolute path", func(t *testing.T) {
		// Create a real executable in a temp dir and put it on PATH so the
		// resolution exercises the LookPath success branch without depending on
		// any particular system binary being present.
		bin := writeExecutable(t)
		dir := filepath.Dir(bin)
		name := filepath.Base(bin)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

		got := resolveAgentBinary(name)
		if got == "" {
			t.Fatalf("resolveAgentBinary(%q) = \"\", want a resolved path", name)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("resolveAgentBinary(%q) = %q, want an absolute path", name, got)
		}
	})
}

// writeExecutable writes a tiny executable script to a temp dir and returns its
// path. On Windows exec.LookPath needs a .bat/.exe extension, so skip there —
// the resolution branch is identical across platforms.
func writeExecutable(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("temp executable script assumes a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "warden-fake-agent")
	script := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentStepRunNoAgent(t *testing.T) {
	ctx := context.Background()
	step := NewAgentStep(domain.StepReview, "review this change")

	for _, agent := range []string{"", "auto"} {
		res, err := step.Run(ctx, application.StepContext{WorktreeDir: t.TempDir(), Agent: agent})
		if err != nil {
			t.Fatalf("Run(agent=%q): %v", agent, err)
		}
		if res.Status != domain.StepPass {
			t.Errorf("Run(agent=%q) status = %s, want pass", agent, res.Status)
		}
		if res.Step != domain.StepReview {
			t.Errorf("Run(agent=%q) step = %s, want review", agent, res.Step)
		}
		if !strings.Contains(res.Summary, "skipped") {
			t.Errorf("Run(agent=%q) summary = %q, want it to mention skipped", agent, res.Summary)
		}
	}
}

func TestAgentStepRunUnavailableAgentSkips(t *testing.T) {
	// An explicitly named agent that isn't installed resolves to "" and so is an
	// advisory pass — the pipeline still runs the deterministic steps.
	ctx := context.Background()
	step := NewAgentStep(domain.StepIntent, "summarize intent")

	res, err := step.Run(ctx, application.StepContext{
		WorktreeDir: t.TempDir(),
		Agent:       "warden-nonexistent-agent-xyz",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass", res.Status)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none for a skipped step", res.Findings)
	}
}

func TestAgentStepRunAgentFails(t *testing.T) {
	// A resolvable agent binary that always exits non-zero drives the resilience
	// chain (retry + circuit breaker) to exhaustion, so the step reports a
	// finding rather than an operational error. This is a fake binary, not a real
	// coding agent.
	if runtime.GOOS == "windows" {
		t.Skip("fake agent script assumes a POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "warden-failing-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	step := NewAgentStep(domain.StepReview, "review this change")
	res, err := step.Run(context.Background(), application.StepContext{
		WorktreeDir: t.TempDir(),
		Agent:       "warden-failing-agent",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != domain.StepFail {
		t.Errorf("status = %s, want fail", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", res.Findings)
	}
	if res.Findings[0].Severity != domain.SeverityMedium {
		t.Errorf("severity = %s, want medium", res.Findings[0].Severity)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
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
	step := NewAgentStep(domain.StepDocument, "check docs")
	if step.Name() != domain.StepDocument {
		t.Errorf("Name() = %s, want document", step.Name())
	}
}
