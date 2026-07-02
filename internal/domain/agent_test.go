package domain

import "testing"

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
