package domain

import (
	"slices"
	"strconv"
	"strings"
)

// Agent authorship signals (ADR 0007, §4).
//
// Coding agents leave declarations behind: a trailer in the commit message, a
// marker in the environment of the processes they spawn. warden reads them at
// gate time and records them in the signed note as OBSERVED DECLARATIONS:
// "commit X's message carried trailer T when it was gated", "the gating process
// ran with marker M". Neither says who wrote the code. A trailer can be typed by
// a person, removed by an agent, or switched off in the agent's settings; a
// marker says what launched the process, not who asked for the push.
//
// So the signals only ever tighten. Under strict@1 a declared agent requires an
// Agent Trace; the absence of every signal relaxes nothing, and is never
// recorded or reported as human authorship.
//
// The tables are built in and not configurable in @1 (ADR 0007 Q12): every
// config field has a compatibility cost, and a missing pattern only drops the
// trace requirement for that commit, never a check. Each entry is taken from
// the tool's own documentation, cited beside it. A tool whose marker or trailer
// could not be confirmed from its documentation is left out rather than guessed:
// a wrong marker would demand a trace from someone no agent was involved with.

// AgentTrailerRule matches one commit trailer an agent documents writing.
type AgentTrailerRule struct {
	// Agent names the tool that documents the trailer, e.g. "claude-code".
	Agent string
	// Key is the trailer key, compared case-insensitively (git and forges
	// spell Co-Authored-By and Co-authored-by interchangeably).
	Key string
	// AddressSuffix, when set, must end the trailer value, compared
	// case-insensitively: the "<address>" an identity trailer names. The
	// display name before it varies (Claude Code writes the model name), so
	// only the address is matched. Empty matches any value of Key.
	AddressSuffix string
	// Source is the documentation the entry is taken from.
	Source string
}

// agentTrailerRules is the built-in trailer table.
var agentTrailerRules = []AgentTrailerRule{
	{
		// "Default: unset, so Claude Code adds `Co-Authored-By: <name>
		// <noreply@anthropic.com>`. The name is the model in use…"
		Agent: "claude-code", Key: "Co-Authored-By", AddressSuffix: "<noreply@anthropic.com>",
		Source: "https://code.claude.com/docs/en/settings-reference#attribution-commit",
	},
	{
		// "Claude Code adds the link as a `Claude-Session` trailer on commits"
		// (cloud and Remote Control sessions).
		Agent: "claude-code", Key: "Claude-Session",
		Source: "https://code.claude.com/docs/en/settings-reference#attribution-sessionurl",
	},
}

// AgentEnvMarkerRule matches one environment variable an agent harness
// documents setting in the processes it spawns.
type AgentEnvMarkerRule struct {
	// Agent names the harness, e.g. "gemini-cli".
	Agent string
	// Name is the variable name, compared exactly.
	Name string
	// Value is the documented value. A variable set to anything else is not
	// the marker: a user may export the same name for their own reasons.
	Value string
	// Source is the documentation the entry is taken from.
	Source string
}

// agentEnvMarkerRules is the built-in environment marker table.
//
// CLAUDECODE is deliberately absent: its documentation says IDE extensions also
// set it in their integrated terminals, so a person typing `git push` in such a
// terminal would read as an agent. CLAUDE_CODE_CHILD_SESSION is "only set by
// Claude Code itself when it launches a subprocess and not by IDE extensions".
// Codex documents no variable it sets for the commands it runs, so it has no
// entry.
var agentEnvMarkerRules = []AgentEnvMarkerRule{
	{
		Agent: "claude-code", Name: "CLAUDE_CODE_CHILD_SESSION", Value: "1",
		Source: "https://code.claude.com/docs/en/env-vars",
	},
	{
		// "Set automatically to `true` when Claude Code is running as a cloud
		// session. Read this from a hook or setup script…"
		Agent: "claude-code", Name: "CLAUDE_CODE_REMOTE", Value: "true",
		Source: "https://code.claude.com/docs/en/env-vars",
	},
	{
		// "When `run_shell_command` executes a command, it sets the
		// `GEMINI_CLI=1` environment variable in the subprocess's environment."
		Agent: "gemini-cli", Name: "GEMINI_CLI", Value: "1",
		Source: "https://geminicli.com/docs/tools/shell",
	},
}

// AgentTrailerRules returns the built-in trailer table, for docs and tests.
func AgentTrailerRules() []AgentTrailerRule { return slices.Clone(agentTrailerRules) }

// AgentEnvMarkerRules returns the built-in environment marker table.
func AgentEnvMarkerRules() []AgentEnvMarkerRule { return slices.Clone(agentEnvMarkerRules) }

// Trailer is one key/value trailer of a commit message, as git parsed it.
type Trailer struct {
	Key   string
	Value string
}

// CommitTrailers is the trailer block of one commit.
type CommitTrailers struct {
	Commit   string
	Trailers []Trailer
}

// TrailerDeclaration is a trailer an agent documents writing, observed on a
// commit in the gated span.
type TrailerDeclaration struct {
	// Commit is the full SHA of the commit whose message carried the trailer.
	Commit string `json:"commit"`
	// Agent is the tool whose documented trailer matched.
	Agent string `json:"agent"`
	// Trailer is the trailer as git parsed it, "Key: value".
	Trailer string `json:"trailer"`
}

// EnvMarkerDeclaration is an agent harness marker observed in the environment
// of the warden process that ran the gate.
type EnvMarkerDeclaration struct {
	// Name is the variable name. Its value is the documented one; it is not
	// recorded, since only the name and value pair from the table can match.
	Name string `json:"name"`
	// Agent is the harness that documents setting it.
	Agent string `json:"agent"`
}

// AuthorshipSignals is what warden observed of agent declarations during a
// profiled run. It is signed, and it is a record of declarations, not of
// authorship: it never says a person wrote anything, and a profiled note
// without it says only that no declaration in the built-in tables was seen.
type AuthorshipSignals struct {
	// Trailers lists matching trailers over the commits the record attests:
	// (CoversFrom, CommitSHA], or CommitSHA alone when no span is claimed.
	Trailers []TrailerDeclaration `json:"trailers,omitempty"`
	// EnvMarkers lists matching markers in the gating process's environment.
	EnvMarkers []EnvMarkerDeclaration `json:"env_markers,omitempty"`
}

// Declared reports whether any agent declaration was observed.
func (s *AuthorshipSignals) Declared() bool {
	return s != nil && (len(s.Trailers) > 0 || len(s.EnvMarkers) > 0)
}

// Summary names the first few declarations, for an error message.
func (s *AuthorshipSignals) Summary() string {
	if !s.Declared() {
		return ""
	}
	const limit = 3
	var parts []string
	for _, t := range s.Trailers {
		parts = append(parts, "commit "+shortSHA(t.Commit)+" carries "+strconv.Quote(t.Trailer))
	}
	for _, m := range s.EnvMarkers {
		parts = append(parts, "the gate runs under "+m.Name+" ("+m.Agent+")")
	}
	if len(parts) > limit {
		more := len(parts) - limit
		parts = append(parts[:limit], "and "+strconv.Itoa(more)+" more")
	}
	return strings.Join(parts, "; ")
}

// ObserveAuthorship matches commits' trailers and an environment against the
// built-in tables. It returns nil when nothing matched, so a note records the
// field only when there is a declaration to record.
//
// environ is in os.Environ form ("NAME=value"). Only names in the table are
// examined; nothing else from the environment reaches the result.
func ObserveAuthorship(commits []CommitTrailers, environ []string) *AuthorshipSignals {
	var s AuthorshipSignals
	for _, c := range commits {
		for _, t := range c.Trailers {
			if r, ok := matchTrailer(t); ok {
				s.Trailers = append(s.Trailers, TrailerDeclaration{
					Commit: c.Commit, Agent: r.Agent, Trailer: t.Key + ": " + t.Value,
				})
			}
		}
	}
	s.EnvMarkers = matchEnvMarkers(environ)
	if !s.Declared() {
		return nil
	}
	return &s
}

func matchTrailer(t Trailer) (AgentTrailerRule, bool) {
	key := strings.TrimSpace(t.Key)
	value := strings.ToLower(strings.TrimSpace(t.Value))
	for _, r := range agentTrailerRules {
		if !strings.EqualFold(key, r.Key) {
			continue
		}
		if r.AddressSuffix == "" || strings.HasSuffix(value, strings.ToLower(r.AddressSuffix)) {
			return r, true
		}
	}
	return AgentTrailerRule{}, false
}

func matchEnvMarkers(environ []string) []EnvMarkerDeclaration {
	values := map[string]string{}
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok {
			values[name] = value
		}
	}
	var out []EnvMarkerDeclaration
	for _, r := range agentEnvMarkerRules {
		if v, ok := values[r.Name]; ok && v == r.Value {
			out = append(out, EnvMarkerDeclaration{Name: r.Name, Agent: r.Agent})
		}
	}
	return out
}
