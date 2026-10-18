package domain

import (
	"slices"
	"strings"
)

// agentPresets maps well-known coding-agent names to a default command template
// (expanding {prompt}/{step}/{repo}). They let `agent: { review: claude }` work
// out of the box without also writing an agent_commands entry. An explicit
// agent_commands entry always overrides the preset. Only agents with a known,
// stable non-interactive invocation are bundled; anything else needs a command.
var agentPresets = map[string]string{
	"claude": "claude -p {prompt}",  // Claude Code, print/headless mode
	"codex":  "codex exec {prompt}", // OpenAI Codex CLI, non-interactive exec
}

// agentBaseEnv is what every agent step keeps when a compliance profile
// allowlists its environment (strict@1): enough for a CLI to find its binary,
// its home directory and the user's locale, and nothing that carries a secret.
// LC_* is matched as a prefix (see AgentEnv).
var agentBaseEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "TMPDIR", "SHELL"}

// agentPresetEnv is the per-preset credential and configuration table: the
// variables a bundled agent's own CLI documents for authenticating and locating
// its state, added to agentBaseEnv when the environment is allowlisted. Every
// name here is taken from the vendor's documentation; a name that is not
// documented is not listed, because an allowlist that guesses is a list of
// secrets warden hands an agent on hope.
//
// Deliberately absent: provider switches that route a CLI through a cloud
// account (CLAUDE_CODE_USE_BEDROCK, CLAUDE_CODE_USE_VERTEX,
// CLAUDE_CODE_USE_FOUNDRY). They only work alongside ambient cloud credentials
// (AWS_*, GOOGLE_*, Azure), which are exactly what the allowlist exists to keep
// from an agent. A repository that needs them cannot use strict@1 yet; an
// explicit `agent_env:` is the planned route (ADR 0007).
var agentPresetEnv = map[string][]string{
	// Claude Code, https://code.claude.com/docs/en/env-vars (checked 2026-10-19):
	//   ANTHROPIC_API_KEY       API key sent as X-Api-Key
	//   ANTHROPIC_AUTH_TOKEN    custom Authorization header value (Bearer)
	//   ANTHROPIC_BASE_URL      override the API endpoint (proxy or gateway)
	//   CLAUDE_CODE_OAUTH_TOKEN OAuth access token for claude.ai authentication,
	//                           the documented alternative to /login for
	//                           automated environments
	//   CLAUDE_CONFIG_DIR       configuration directory (default ~/.claude)
	// A subscription login stored by /login is read from the OS keychain or
	// from under the config directory, which HOME and USER already reach.
	"claude": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"},
	// OpenAI Codex CLI, https://developers.openai.com/codex/environment-variables
	// (checked 2026-10-19):
	//   CODEX_HOME           root for Codex state, including config and auth
	//                        (default ~/.codex)
	//   CODEX_API_KEY        API key for a non-interactive run (codex exec)
	//   CODEX_ACCESS_TOKEN   ChatGPT or Codex access token for trusted automation
	//   CODEX_CA_CERTIFICATE PEM CA bundle for TLS interception / private roots
	//   SSL_CERT_FILE        fallback CA bundle when CODEX_CA_CERTIFICATE is unset
	// OPENAI_API_KEY is NOT on the list: that page says Codex does not read a
	// provider key by a fixed name; it reads the variable a model provider's
	// env_key names, or a key stored with `codex login --with-api-key`
	// (https://learn.chatgpt.com/docs/auth), which CODEX_HOME reaches.
	"codex": {"CODEX_HOME", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE"},
}

// AgentPreset returns the bundled command template for a known agent, or "".
func AgentPreset(name string) string { return agentPresets[name] }

// BundledAgents lists the agent names that ship with a preset command.
func BundledAgents() []string { return []string{"claude", "codex"} }

// IsBundledAgent reports whether name is a bundled preset.
func IsBundledAgent(name string) bool { return agentPresets[name] != "" }

// AgentEnvNames returns the variable names an allowlisted agent step keeps for
// agent: the base set plus the preset's own table. LC_* is implied. A custom
// agent gets the base set only.
func AgentEnvNames(agent string) []string {
	return append(slices.Clone(agentBaseEnv), agentPresetEnv[agent]...)
}

// AgentEnv filters environ ("KEY=value" entries) down to what an allowlisted
// agent step may see: the base set, every LC_* locale variable, and the
// preset's documented credential and configuration variables. Everything else
// is dropped: cloud credentials, forge tokens, other tools' API keys, and the
// GIT_* variables git exports into a hook.
func AgentEnv(agent string, environ []string) []string {
	keep := AgentEnvNames(agent)
	var out []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(keep, name) || strings.HasPrefix(name, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// ResolveAgentCommand picks the command for an agent: an explicit
// agent_commands entry wins, otherwise a bundled preset, otherwise "" (which
// makes the agent step an advisory skip).
func ResolveAgentCommand(configured map[string]string, agent string) string {
	if c, ok := configured[agent]; ok && strings.TrimSpace(c) != "" {
		return c
	}
	return AgentPreset(agent)
}

// AgentBoundary is what a compliance profile enforces around one agent step at
// run time (ADR 0007 §3). The zero value enforces nothing, which is what every
// step gets without a profile, under permissive, and for a step that is not an
// agent step.
type AgentBoundary struct {
	// Profile names the profile in force ("strict@1"), for messages.
	Profile string
	// RequireExecution fails the step, rather than skipping it, when no agent
	// or no command is configured to perform it.
	RequireExecution bool
	// Isolate runs the step in a disposable clone of the validated worktree and
	// guards the tracked tree, the index and HEAD of both across the step. A
	// clone that cannot be made fails the step instead of falling back.
	Isolate bool
	// EnvAllowlist hands the agent only AgentEnv's variables, and refuses an
	// agent that is not a bundled preset (its needs are unknown).
	EnvAllowlist bool
}

// Active reports whether the boundary enforces anything.
func (b AgentBoundary) Active() bool {
	return b.RequireExecution || b.Isolate || b.EnvAllowlist
}

// AgentBoundaryObservation is what warden observed about an agent step's
// boundary, under a profile that enforces one. It is reported on the step
// result for delivery surfaces and for the signed record to adopt; it is not
// part of the evidence chain. A false field means "not observed", never "the
// agent did the opposite": an agent step that was not isolated has no guard
// result to report.
type AgentBoundaryObservation struct {
	// Isolated: the step ran in its own disposable clone, not in the worktree
	// whose tree is attested.
	Isolated bool `json:"isolated"`
	// TreeUnchanged: the clone's and the validated worktree's HEAD, index tree
	// and tracked files were the same after the step as before it. Untracked
	// output is allowed and not compared.
	TreeUnchanged bool `json:"tree_unchanged"`
	// EnvAllowlisted: the agent process saw only the allowlisted environment.
	EnvAllowlisted bool `json:"env_allowlisted"`
}

// TreeState is a worktree's tracked state, for the agent tree guard: HEAD,
// the tree the index would write, and a digest of the tracked files' unstaged
// changes. Two equal states mean nothing tracked moved; untracked files do not
// take part.
type TreeState struct {
	Head     string
	Index    string
	Unstaged string
}

// Describe names what differs between two tree states, for a guard failure.
// It returns "" when they are equal.
func (s TreeState) Describe(after TreeState) string {
	var parts []string
	if s.Head != after.Head {
		parts = append(parts, "moved HEAD from "+shortSHA(s.Head)+" to "+shortSHA(after.Head))
	}
	if s.Index != after.Index {
		parts = append(parts, "changed the index")
	}
	if s.Unstaged != after.Unstaged {
		parts = append(parts, "changed tracked files")
	}
	return strings.Join(parts, ", ")
}
