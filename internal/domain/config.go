package domain

// Config is the parsed .warden.yaml (§5.1). Field tags mirror the documented
// YAML surface exactly. Zero values are meaningful: an omitted section falls
// back to the documented defaults during policy resolution, not here.
type Config struct {
	// Agent is the default coding-agent selection ("auto" or a binary name).
	Agent string `yaml:"agent"`

	Hooks HookConfig `yaml:"hooks"`

	// Commands maps a built-in shell-backed step to the command it runs.
	Commands map[string]string `yaml:"commands"`

	// Timeouts maps a step to a max duration (e.g. "5m", "30s"). A step that
	// exceeds it is killed and fails, so a wedged test or agent can't hang the
	// gate. Unset or unparseable = no timeout.
	Timeouts map[string]string `yaml:"timeouts"`

	// Cache maps a step to the input path globs it depends on. When every matched
	// file is byte-identical to the step's last passing run, warden skips the
	// step (a cache hit). Only non-mutating steps are cacheable; correctness
	// rests on declaring all of a step's inputs (like bazel/turbo).
	Cache map[string][]string `yaml:"cache"`

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

	Risk RiskConfig `yaml:"risk"`

	// PR configures optional pull-request creation after a passing push.
	PR PRConfig `yaml:"pr"`

	Rules []Rule `yaml:"rules"`
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
