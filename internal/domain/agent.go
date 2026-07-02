package domain

import "strings"

// agentPresets maps well-known coding-agent names to a default command template
// (expanding {prompt}/{step}/{repo}). They let `agent: { review: claude }` work
// out of the box without also writing an agent_commands entry. An explicit
// agent_commands entry always overrides the preset. Only agents with a known,
// stable non-interactive invocation are bundled; anything else needs a command.
var agentPresets = map[string]string{
	"claude": "claude -p {prompt}",  // Claude Code, print/headless mode
	"codex":  "codex exec {prompt}", // OpenAI Codex CLI, non-interactive exec
}

// AgentPreset returns the bundled command template for a known agent, or "".
func AgentPreset(name string) string { return agentPresets[name] }

// BundledAgents lists the agent names that ship with a preset command.
func BundledAgents() []string { return []string{"claude", "codex"} }

// ResolveAgentCommand picks the command for an agent: an explicit
// agent_commands entry wins, otherwise a bundled preset, otherwise "" (which
// makes the agent step an advisory skip).
func ResolveAgentCommand(configured map[string]string, agent string) string {
	if c, ok := configured[agent]; ok && strings.TrimSpace(c) != "" {
		return c
	}
	return AgentPreset(agent)
}
