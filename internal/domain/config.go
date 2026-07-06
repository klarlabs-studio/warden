package domain

import (
	"fmt"
	"time"
)

// Config is the parsed .warden.yaml (§5.1). Field tags mirror the documented
// YAML surface exactly. Zero values are meaningful: an omitted section falls
// back to the documented defaults during policy resolution, not here.
type Config struct {
	// Extends names a base config to inherit from (a path relative to this
	// config, or absolute). The base is loaded first and this config overlays it,
	// so an org can share one .warden.yaml across repos and each repo overrides
	// only what it needs.
	Extends string `yaml:"extends"`

	// Agent is the default coding-agent selection ("auto" or a binary name).
	Agent string `yaml:"agent"`

	Hooks HookConfig `yaml:"hooks"`

	// Commands maps a built-in shell-backed step to the command it runs.
	Commands map[string]string `yaml:"commands"`

	// Timeouts maps a step to a max duration (e.g. "5m", "30s"). A step that
	// exceeds it is killed and fails, so a wedged test or agent can't hang the
	// gate. Unset or "0" = no timeout; a malformed or negative value is rejected
	// at config load (see Validate) rather than silently meaning "no limit".
	Timeouts map[string]string `yaml:"timeouts"`

	// Cache maps a step to the input path globs it depends on. When every matched
	// file is byte-identical to the step's last passing run, warden skips the
	// step (a cache hit). Only non-mutating steps are cacheable; correctness
	// rests on declaring all of a step's inputs (like bazel/turbo).
	Cache map[string][]string `yaml:"cache"`

	// SymlinkDeps opts out of dependency materialization. By default warden
	// hardlink-copies gitignored dependency directories (node_modules) into the
	// disposable worktree as REAL files, so any tool works — including Next.js 16
	// / Turbopack, which rejects a node_modules symlink whose target resolves
	// outside the worktree root. Hardlinks are near-instant on the same
	// filesystem (and fall back to a byte copy across filesystems). Set
	// `symlink_deps: true` to force the old fast symlink instead — fine for
	// tsc/eslint/vitest/Node (which follow symlinks) and cheaper when node_modules
	// is large and the worktree temp dir is on a different filesystem.
	SymlinkDeps *bool `yaml:"symlink_deps"`

	// MaterializeDeps is deprecated: materialization is now the default (see
	// SymlinkDeps). The field is still parsed for backward compatibility but no
	// longer changes behavior.
	MaterializeDeps []string `yaml:"materialize_deps"`

	// Writes lists steps that mutate the worktree and must therefore run as
	// sequential barriers rather than in a parallel batch. Warden already treats
	// rebase, auto-fix, and coding-agent steps as writers; use this for a custom
	// step (a codegen or formatter command) that edits tracked files, so a
	// concurrent lint/test never reads a half-written tree. e.g. `writes: [codegen]`.
	Writes []string `yaml:"writes"`

	// AgentCommands maps an agent name (as selected by `agent` or a rule's
	// per-step agent override) to the shell command that invokes it. The
	// template may reference {prompt}, {step}, and {repo}; e.g.
	//   agent_commands: { claude: "claude -p {prompt}", codex: "codex exec {prompt}" }
	// An agent step with no matching command is an advisory skip — Warden never
	// guesses a coding-agent CLI contract.
	AgentCommands map[string]string `yaml:"agent_commands"`

	// Steps lists the step subset per hook. Keys are "pre_commit"/"pre_push".
	Steps map[string][]StepName `yaml:"steps"`

	// Parallel toggles concurrent execution of independent (read-only) steps.
	// Unset (nil) defaults to enabled; set `parallel: false` to force the
	// classic one-step-at-a-time pipeline.
	Parallel *bool `yaml:"parallel"`

	// Notify toggles a desktop notification when an interactive pre-push run
	// finishes. Unset (nil) defaults to enabled; set `notify: false` to silence.
	Notify *bool `yaml:"notify"`

	// NotifyAfter is the minimum run duration before a PASSING interactive
	// pre-push fires a desktop notification (e.g. "30s", "2m"). Empty defaults
	// to 10s. A failed/blocked push always notifies regardless of duration.
	// Ignored when notify is false. A malformed or negative value is rejected at
	// config load (see Validate) rather than silently ignored.
	NotifyAfter string `yaml:"notify_after"`

	Risk RiskConfig `yaml:"risk"`

	// PR configures optional pull-request creation after a passing push.
	PR PRConfig `yaml:"pr"`

	Rules []Rule `yaml:"rules"`
}

// Validate rejects a parsed config that carries an unsafe step name. Every
// StepName that can enter the resolved pipeline — the per-hook `steps:` lists
// and the step names named by rules (auto_fix, per-step agent, add/skip/
// insert_after) — must pass StepName.Valid so a custom step cannot smuggle a
// path separator or shell metacharacter into the custom-step binary lookup. It
// is called by the config loader after parsing so an invalid name fails the
// load rather than reaching exec.LookPath. Built-in names always pass.
func (c Config) Validate() error {
	for hook, names := range c.Steps {
		for _, n := range names {
			if !n.Valid() {
				return fmt.Errorf("invalid step name %q in steps.%s: names must match [a-zA-Z0-9][a-zA-Z0-9_-]*", string(n), hook)
			}
		}
	}
	for _, n := range c.Writes {
		if !StepName(n).Valid() {
			return fmt.Errorf("invalid step name %q in writes", n)
		}
	}
	for i, r := range c.Rules {
		for n := range r.Then.AutoFix {
			if !n.Valid() {
				return fmt.Errorf("invalid step name %q in rules[%d].then.auto_fix", string(n), i)
			}
		}
		for n := range r.Then.Agent {
			if !n.Valid() {
				return fmt.Errorf("invalid step name %q in rules[%d].then.agent", string(n), i)
			}
		}
		for hook, edit := range r.Then.Steps {
			for _, n := range edit.Add {
				if !n.Valid() {
					return fmt.Errorf("invalid step name %q in rules[%d].then.steps.%s.add", string(n), i, hook)
				}
			}
			for _, n := range edit.Skip {
				if !n.Valid() {
					return fmt.Errorf("invalid step name %q in rules[%d].then.steps.%s.skip", string(n), i, hook)
				}
			}
			if edit.InsertAfter != "" && !edit.InsertAfter.Valid() {
				return fmt.Errorf("invalid step name %q in rules[%d].then.steps.%s.insert_after", string(edit.InsertAfter), i, hook)
			}
		}
	}
	// notify_after, when set, must be a valid non-negative Go duration. Rejecting
	// it here — at load, next to the other field checks — surfaces a typo (e.g.
	// "10" with no unit, or "10ss") as a clear config error instead of silently
	// snapping back to the 10s default and leaving the operator to wonder why
	// their threshold never took effect.
	if c.NotifyAfter != "" {
		if err := validateDuration("notify_after", c.NotifyAfter); err != nil {
			return err
		}
	}
	// Each timeout, when set, must likewise be a valid non-negative Go duration.
	// The stakes are higher than notify_after: a malformed value ("30" with no
	// unit, "5mm") parses to nothing downstream and silently means "no limit", so
	// a wedged step would hang the gate unbounded — the exact opposite of what a
	// safety timeout is for. "0" is allowed as the explicit no-limit marker.
	for step, s := range c.Timeouts {
		if err := validateDuration(fmt.Sprintf("timeout for step %q", step), s); err != nil {
			return err
		}
	}
	return nil
}

// validateDuration reports why a config duration string is unusable, or nil if
// it is a valid non-negative Go duration. field names the setting for the error
// (e.g. "notify_after"). Empty is the caller's to allow or reject before calling.
func validateDuration(field, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q: must be a Go duration such as \"30s\" or \"5m\": %w", field, value, err)
	}
	if d < 0 {
		return fmt.Errorf("invalid %s %q: must not be negative", field, value)
	}
	return nil
}

// HookConfig records which hooks are installed (§4.1).
type HookConfig struct {
	PreCommit bool `yaml:"pre_commit"`
	PrePush   bool `yaml:"pre_push"`
}

// Enabled reports whether the given hook is switched on.
func (h HookConfig) Enabled(hook Hook) bool {
	switch hook {
	case PreCommit:
		return h.PreCommit
	case PrePush:
		return h.PrePush
	default:
		return false
	}
}

// RiskConfig carries the tunable risk thresholds.
type RiskConfig struct {
	DiffLinesHigh    int `yaml:"diff_lines_high"`
	FilesTouchedHigh int `yaml:"files_touched_high"`
}

// Thresholds resolves the config to a RiskThresholds, substituting documented
// defaults for any unset (zero) field.
func (r RiskConfig) Thresholds() RiskThresholds {
	t := DefaultRiskThresholds()
	if r.DiffLinesHigh > 0 {
		t.DiffLinesHigh = r.DiffLinesHigh
	}
	if r.FilesTouchedHigh > 0 {
		t.FilesTouchedHigh = r.FilesTouchedHigh
	}
	return t
}

// Rule is a single policy rule: match conditions and the overrides they apply
// when all conditions hold (§5.2).
type Rule struct {
	Match Match `yaml:"match"`
	Then  Then  `yaml:"then"`
}

// Match holds the conditions of a rule. A rule matches when every set field is
// satisfied. An unset field is not a condition.
type Match struct {
	Branch string   `yaml:"branch"`
	Paths  []string `yaml:"paths"`
	Risk   Risk     `yaml:"risk"`
}

// Then holds the overrides a matching rule contributes.
type Then struct {
	// AutoFix maps a step to its retry budget (auto_fix.<step>: N).
	AutoFix map[StepName]int `yaml:"auto_fix"`
	// RequireApproval forces an approval pause for the run.
	RequireApproval *bool `yaml:"require_approval"`
	// Agent maps a step to the coding-agent binary it should use.
	Agent map[StepName]string `yaml:"agent"`
	// Steps carries per-hook add/skip/insert directives.
	Steps map[string]StepEdit `yaml:"steps"`
}

// StepEdit mutates the step list for a hook (§5.1). Add is unioned across
// matching rules; Skip removes; InsertAfter positions an Add.
type StepEdit struct {
	Add         []StepName `yaml:"add"`
	Skip        []StepName `yaml:"skip"`
	InsertAfter StepName   `yaml:"insert_after"`
}

// Specificity scores a match for stacking tie-breaks: more conditions set =
// more specific (§5.2). Branch/risk/paths each count once.
func (m Match) Specificity() int {
	n := 0
	if m.Branch != "" {
		n++
	}
	if m.Risk != "" {
		n++
	}
	if len(m.Paths) > 0 {
		n++
	}
	return n
}
