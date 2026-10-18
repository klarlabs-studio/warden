package domain

import (
	"slices"
	"testing"
)

func TestResolveAgentCommand(t *testing.T) {
	// Bundled preset used when no config entry.
	if got := ResolveAgentCommand(nil, "claude"); got != "claude -p {prompt}" {
		t.Errorf("claude preset = %q", got)
	}
	// Explicit config overrides the preset.
	cfg := map[string]string{"claude": "my-claude {prompt}"}
	if got := ResolveAgentCommand(cfg, "claude"); got != "my-claude {prompt}" {
		t.Errorf("config should override preset, got %q", got)
	}
	// Empty config entry falls back to the preset.
	if got := ResolveAgentCommand(map[string]string{"codex": "  "}, "codex"); got != "codex exec {prompt}" {
		t.Errorf("blank config should fall back to preset, got %q", got)
	}
	// Unknown agent has no command.
	if got := ResolveAgentCommand(nil, "mystery"); got != "" {
		t.Errorf("unknown agent should be empty, got %q", got)
	}
	if len(BundledAgents()) != 2 {
		t.Errorf("expected 2 bundled agents, got %v", BundledAgents())
	}
}

func TestAgentEnv_KeepsOnlyTheAllowlist(t *testing.T) {
	environ := []string{
		"PATH=/bin", "HOME=/h", "USER=u", "LOGNAME=u", "LANG=C", "LC_ALL=C", "LC_CTYPE=UTF-8",
		"TERM=xterm", "TMPDIR=/t", "SHELL=/bin/sh",
		"ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "CLAUDE_CODE_OAUTH_TOKEN=o",
		"CODEX_HOME=/x", "CODEX_API_KEY=k",
		"AWS_SECRET_ACCESS_KEY=s", "GITHUB_TOKEN=g", "OPENAI_API_KEY=o", "FAKE_TOKEN=f",
		"GIT_DIR=/repo/.git", "NOEQUALS",
	}
	base := []string{"PATH=/bin", "HOME=/h", "USER=u", "LOGNAME=u", "LANG=C", "LC_ALL=C", "LC_CTYPE=UTF-8", "TERM=xterm", "TMPDIR=/t", "SHELL=/bin/sh"}
	for agent, want := range map[string][]string{
		"claude": append(slices.Clone(base), "ANTHROPIC_API_KEY=a", "CLAUDE_CONFIG_DIR=/c", "CLAUDE_CODE_OAUTH_TOKEN=o"),
		"codex":  append(slices.Clone(base), "CODEX_HOME=/x", "CODEX_API_KEY=k"),
		"faux":   base,
	} {
		if got := AgentEnv(agent, environ); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", agent, got, want)
		}
	}
}

// The preset tables name only documented variables; this pins them so a change
// is a reviewed diff against the sources cited in agent.go.
func TestAgentEnvNames_Pinned(t *testing.T) {
	for agent, want := range map[string][]string{
		"claude": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"},
		"codex":  {"CODEX_HOME", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"},
	} {
		got := AgentEnvNames(agent)[len(agentBaseEnv):]
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v", agent, got)
		}
	}
	for _, a := range BundledAgents() {
		if _, ok := agentPresetEnv[a]; !ok || !IsBundledAgent(a) {
			t.Errorf("bundled agent %s has no credential table", a)
		}
	}
}

func TestTreeState_Describe(t *testing.T) {
	a := TreeState{Head: "aaaaaaaaaaaaaaaa", Index: "t1", Unstaged: "d1"}
	if d := a.Describe(a); d != "" {
		t.Errorf("equal states differ: %q", d)
	}
	b := TreeState{Head: "bbbbbbbbbbbbbbbb", Index: "t2", Unstaged: "d2"}
	if d := a.Describe(b); d != "moved HEAD from aaaaaaaaaaaa to bbbbbbbbbbbb, changed the index, changed tracked files" {
		t.Errorf("describe = %q", d)
	}
}
