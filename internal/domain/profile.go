package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ComplianceProfile is a built-in, versioned, immutable bundle of config
// defaults and floors, selected with the top-level `profile:` key (ADR 0007).
//
// It is named ComplianceProfile rather than Profile because the kernel already
// uses "profile" for axi effect and idempotency profiles, which have nothing to
// do with compliance.
//
// A profile is applied in two parts. Base() is the implicit lowest layer of the
// extends chain, so its defaults apply unless a file overrides them. Floors are
// then checked against the merged effective config; a config that does not meet
// them fails to load. Neither part can be redefined by a repository: the
// definitions live in this file, and a published definition never changes. A
// bare name resolves to version 1 forever; tightening ships as a new version
// that a repository adopts in a reviewed diff.
type ComplianceProfile struct {
	def profileDefinition
}

// ProfileLevel orders the profiles: permissive < standard < strict. The order
// is what lets `extends:` ratchet a profile upward and lets a verifier ask for
// "at least standard".
type ProfileLevel int

// The three levels. The zero value means "no profile".
const (
	ProfilePermissive ProfileLevel = iota + 1
	ProfileStandard
	ProfileStrict
)

// String returns the level's configuration name.
func (l ProfileLevel) String() string {
	switch l {
	case ProfilePermissive:
		return "permissive"
	case ProfileStandard:
		return "standard"
	case ProfileStrict:
		return "strict"
	default:
		return ""
	}
}

// DefaultAgentTimeout is the timeout standard@1 and strict@1 give a built-in
// agent step that sets none, so a wedged agent cannot hang the gate unbounded.
const DefaultAgentTimeout = "10m"

// profileDefinition is the whole of what a profile version means. Its JSON form
// is what Digest hashes, so every field that changes behavior must be in it, and
// once a version is published its value must never change: the golden digests
// in profile_test.go fail if it does.
type profileDefinition struct {
	Name    string       `json:"name"`
	Version int          `json:"version"`
	Level   ProfileLevel `json:"level"`

	// RequiredSteps lists, per hook config key, steps the effective step list
	// must contain.
	RequiredSteps map[string][]StepName `json:"required_steps,omitempty"`
	// NoSkipRequired refuses a rule that skips a required step.
	NoSkipRequired bool `json:"no_skip_required,omitempty"`
	// RequireCommands refuses a required shell step with no command.
	RequireCommands bool `json:"require_commands,omitempty"`
	// ContainerExecution requires shell checks to resolve to a container:
	// execution.mode is not host and execution.image is set.
	ContainerExecution bool `json:"container_execution,omitempty"`
	// NetworkNone requires execution.network to be none.
	NetworkNone bool `json:"network_none,omitempty"`
	// NoAgentWrites refuses auto_fix and kept writes on agent steps.
	NoAgentWrites bool `json:"no_agent_writes,omitempty"`
	// AgentTimeout is the default timeout for agent steps that set none.
	AgentTimeout string `json:"agent_timeout,omitempty"`
	// AgentTimeoutRequired refuses an agent step timeout of "0" (no limit).
	AgentTimeoutRequired bool `json:"agent_timeout_required,omitempty"`
	// SigningRequired defaults signing.required to true.
	SigningRequired bool `json:"signing_required,omitempty"`
}

// builtinProfiles holds every published definition, keyed by "name@version".
// Entries are append-only: add a new version, never edit an old one (see the
// golden digests in profile_test.go for the one window ADR 0007 leaves open).
var builtinProfiles = map[string]profileDefinition{
	"permissive@1": {Name: "permissive", Version: 1, Level: ProfilePermissive},
	"standard@1": {
		Name: "standard", Version: 1, Level: ProfileStandard,
		RequiredSteps: map[string][]StepName{
			PrePush.ConfigKey():   {StepCredentials, StepLint, StepTest},
			PreCommit.ConfigKey(): {StepCredentials},
		},
		NoSkipRequired:  true,
		RequireCommands: true,
		AgentTimeout:    DefaultAgentTimeout,
		SigningRequired: true,
	},
	"strict@1": {
		Name: "strict", Version: 1, Level: ProfileStrict,
		RequiredSteps: map[string][]StepName{
			PrePush.ConfigKey():   {StepCredentials, StepLint, StepTest, StepSecurityScan, StepReview},
			PreCommit.ConfigKey(): {StepCredentials, StepLint},
		},
		NoSkipRequired:       true,
		RequireCommands:      true,
		ContainerExecution:   true,
		NetworkNone:          true,
		NoAgentWrites:        true,
		AgentTimeout:         DefaultAgentTimeout,
		AgentTimeoutRequired: true,
		SigningRequired:      true,
	},
}

// ProfileNames lists the profile levels in order, for messages and docs.
func ProfileNames() []string { return []string{"permissive", "standard", "strict"} }

// ParseComplianceProfile resolves a `profile:` value: a bare name ("strict",
// which always means version 1) or an explicit "name@N". An unknown name or
// version is an error, never a fallback, so a typo cannot quietly select a
// weaker profile or none.
func ParseComplianceProfile(s string) (ComplianceProfile, error) {
	name, version := s, "1"
	if i := strings.IndexByte(s, '@'); i >= 0 {
		name, version = s[:i], s[i+1:]
	}
	n, err := strconv.Atoi(version)
	if err != nil || n < 1 || strconv.Itoa(n) != version {
		return ComplianceProfile{}, fmt.Errorf("invalid profile %q: the version after @ must be a positive number, e.g. strict@1", s)
	}
	def, ok := builtinProfiles[name+"@"+version]
	if !ok {
		if !slices.Contains(ProfileNames(), name) {
			return ComplianceProfile{}, fmt.Errorf("unknown profile %q: known profiles are %s", s, strings.Join(ProfileNames(), ", "))
		}
		return ComplianceProfile{}, fmt.Errorf("unknown profile version %q: this warden knows %s@1; a later version needs a newer warden", s, name)
	}
	return ComplianceProfile{def: def}, nil
}

// Name returns the profile's level name ("strict").
func (p ComplianceProfile) Name() string { return p.def.Name }

// Version returns the definition version (1 for "strict").
func (p ComplianceProfile) Version() int { return p.def.Version }

// Level returns the profile's place in the order.
func (p ComplianceProfile) Level() ProfileLevel { return p.def.Level }

// String returns the canonical "name@version" form.
func (p ComplianceProfile) String() string {
	return p.def.Name + "@" + strconv.Itoa(p.def.Version)
}

// Digest is the stable digest of the definition. A verifier compares it with
// its own built-in table, so a fork that redefines a published version is
// detectable rather than silently honored.
func (p ComplianceProfile) Digest() string {
	d, err := DigestJSON(p.def)
	if err != nil {
		// The definition is a plain struct of strings, ints, bools and maps;
		// it always marshals.
		panic("profile definition does not marshal: " + err.Error())
	}
	return d
}

// Lowers reports whether p is ranked below other: a lower level, or the same
// level at an earlier version.
func (p ComplianceProfile) Lowers(other ComplianceProfile) bool {
	if p.def.Level != other.def.Level {
		return p.def.Level < other.def.Level
	}
	return p.def.Version < other.def.Version
}

// RequiredSteps returns the steps the profile requires for a hook.
func (p ComplianceProfile) RequiredSteps(h Hook) []StepName {
	return slices.Clone(p.def.RequiredSteps[h.ConfigKey()])
}

// Base returns the profile's config defaults: the layer every file in the
// extends chain overlays. It carries only settings a repository may tighten
// further; the floors stop it from being loosened.
//
// Base deliberately sets no steps. Steps merge by union with the base first, so
// a base step list would reorder a repository's own steps (moving rebase after
// the checks, for one) and replace the built-in default list for a hook the
// repository never configured. Required steps are a floor instead: the
// profile refuses a config that lacks them rather than inserting them.
func (p ComplianceProfile) Base() Config {
	var c Config
	c.Profile = p.String()
	if p.def.SigningRequired {
		c.Signing.Required = true
	}
	if p.def.NetworkNone {
		c.Execution.Network = "none"
	}
	if p.def.AgentTimeout != "" {
		c.Timeouts = map[string]string{}
		for _, s := range []StepName{StepIntent, StepReview, StepDocument} {
			c.Timeouts[string(s)] = p.def.AgentTimeout
		}
	}
	return c
}

// Apply returns the effective config with the profile in force: chain (the
// merged extends chain) overlaid onto Base(), plus the default timeout for any
// step a rule hands to an agent, which a static Base cannot know about.
func (p ComplianceProfile) Apply(chain Config) Config {
	out := chain.OverlayOnto(p.Base())
	out.Profile = p.String()
	if p.def.AgentTimeout == "" {
		return out
	}
	for _, s := range agentSteps(out) {
		if _, ok := out.Timeouts[string(s)]; !ok {
			if out.Timeouts == nil {
				out.Timeouts = map[string]string{}
			}
			out.Timeouts[string(s)] = p.def.AgentTimeout
		}
	}
	return out
}

// FloorViolation is one way an effective config falls short of a profile.
type FloorViolation struct {
	// Floor names the floor that failed (see ProfileFloor.ID).
	Floor string
	// Field is the config field at fault, e.g. "execution.mode".
	Field string
	// Message says what is wrong and what would satisfy the floor.
	Message string
	// SetBy reports whether one file's own config set the offending value, so
	// the loader can name the file. Nil when the violation is an absence
	// (nothing set it), which no single file is responsible for.
	SetBy func(Config) bool `json:"-"`
}

// ProfileFloor describes one floor, for `policy explain`.
type ProfileFloor struct {
	ID          string
	Description string
}

// ProfileFloorStatus is a floor and whether a config meets it.
type ProfileFloorStatus struct {
	ID          string
	Description string
	Satisfied   bool
	// Problems lists what is wrong when Satisfied is false.
	Problems []string `json:",omitempty"`
}

// Floor IDs. They are stable strings so explain output and tests can name them.
const (
	FloorRequiredSteps   = "required-steps"
	FloorNoSkipRequired  = "no-skip-required"
	FloorCommands        = "commands-present"
	FloorContainer       = "container-execution"
	FloorNetworkNone     = "network-none"
	FloorNoAgentWrites   = "no-agent-writes"
	FloorAgentTimeout    = "agent-timeout"
	FloorSigningRequired = "signing-required"
)

// Floors lists the floors this profile checks at load, in a fixed order.
func (p ComplianceProfile) Floors() []ProfileFloor {
	var out []ProfileFloor
	d := p.def
	if len(d.RequiredSteps) > 0 {
		out = append(out, ProfileFloor{FloorRequiredSteps, "required steps are listed: pre_push " +
			JoinSteps(d.RequiredSteps[PrePush.ConfigKey()]) + "; pre_commit " + JoinSteps(d.RequiredSteps[PreCommit.ConfigKey()])})
	}
	if d.NoSkipRequired {
		out = append(out, ProfileFloor{FloorNoSkipRequired, "no rule skips a required step"})
	}
	if d.RequireCommands {
		out = append(out, ProfileFloor{FloorCommands, "every required shell step has a command"})
	}
	if d.ContainerExecution {
		out = append(out, ProfileFloor{FloorContainer, "shell checks resolve to a container (execution.mode is not host, execution.image is set)"})
	}
	if d.NetworkNone {
		out = append(out, ProfileFloor{FloorNetworkNone, "execution.network is none"})
	}
	if d.NoAgentWrites {
		out = append(out, ProfileFloor{FloorNoAgentWrites, "no auto_fix or writes: entry on an agent step"})
	}
	if d.AgentTimeoutRequired {
		out = append(out, ProfileFloor{FloorAgentTimeout, "every agent step has a timeout (\"0\" is refused)"})
	}
	if d.SigningRequired {
		out = append(out, ProfileFloor{FloorSigningRequired, "signing.required is true"})
	}
	return out
}

// Check returns every floor the effective config cfg fails, in floor order.
// cfg must be the merged config with Base already applied (see Apply).
func (p ComplianceProfile) Check(cfg Config) []FloorViolation {
	var out []FloorViolation
	for _, f := range p.Floors() {
		out = append(out, p.checkFloor(f.ID, cfg)...)
	}
	return out
}

// FloorStatus reports each floor and whether cfg meets it, for explain.
func (p ComplianceProfile) FloorStatus(cfg Config) []ProfileFloorStatus {
	floors := p.Floors()
	out := make([]ProfileFloorStatus, 0, len(floors))
	for _, f := range floors {
		st := ProfileFloorStatus{ID: f.ID, Description: f.Description, Satisfied: true}
		for _, v := range p.checkFloor(f.ID, cfg) {
			st.Satisfied = false
			st.Problems = append(st.Problems, v.Field+": "+v.Message)
		}
		out = append(out, st)
	}
	return out
}

func (p ComplianceProfile) checkFloor(id string, cfg Config) []FloorViolation {
	switch id {
	case FloorRequiredSteps:
		return p.checkRequiredSteps(cfg)
	case FloorNoSkipRequired:
		return p.checkNoSkip(cfg)
	case FloorCommands:
		return p.checkCommands(cfg)
	case FloorContainer:
		return checkContainer(cfg)
	case FloorNetworkNone:
		return checkNetwork(cfg)
	case FloorNoAgentWrites:
		return checkAgentWrites(cfg)
	case FloorAgentTimeout:
		return checkAgentTimeouts(cfg)
	case FloorSigningRequired:
		if !cfg.Signing.Required {
			return []FloorViolation{{Floor: id, Field: "signing.required", Message: "must be true"}}
		}
	}
	return nil
}

// hooksInOrder fixes iteration order so violations are deterministic.
var hooksInOrder = []Hook{PreCommit, PrePush}

func (p ComplianceProfile) checkRequiredSteps(cfg Config) []FloorViolation {
	var out []FloorViolation
	for _, h := range hooksInOrder {
		listed := EffectiveHookSteps(cfg, h)
		for _, s := range p.def.RequiredSteps[h.ConfigKey()] {
			if slices.Contains(listed, s) {
				continue
			}
			out = append(out, FloorViolation{
				Floor: FloorRequiredSteps, Field: "steps." + h.ConfigKey(),
				Message: fmt.Sprintf("must include %s; it resolves to [%s]", s, JoinSteps(listed)),
			})
		}
	}
	return out
}

func (p ComplianceProfile) checkNoSkip(cfg Config) []FloorViolation {
	var out []FloorViolation
	for i, r := range cfg.Rules {
		for _, h := range hooksInOrder {
			key := h.ConfigKey()
			edit, ok := r.Then.Steps[key]
			if !ok {
				continue
			}
			for _, s := range edit.Skip {
				if !slices.Contains(p.def.RequiredSteps[key], s) {
					continue
				}
				out = append(out, FloorViolation{
					Floor: FloorNoSkipRequired, Field: fmt.Sprintf("rules[%d].then.steps.%s.skip", i, key),
					Message: fmt.Sprintf("skips %s, which the profile requires", s),
					SetBy:   ruleSkips(key, s),
				})
			}
		}
	}
	return out
}

// ruleSkips reports whether a file has a rule skipping step s for hook key.
func ruleSkips(key string, s StepName) func(Config) bool {
	return func(c Config) bool {
		for _, r := range c.Rules {
			if slices.Contains(r.Then.Steps[key].Skip, s) {
				return true
			}
		}
		return false
	}
}

func (p ComplianceProfile) checkCommands(cfg Config) []FloorViolation {
	var out []FloorViolation
	seen := map[StepName]bool{}
	for _, h := range hooksInOrder {
		for _, s := range p.def.RequiredSteps[h.ConfigKey()] {
			if seen[s] || !needsCommand(s) {
				continue
			}
			seen[s] = true
			if strings.TrimSpace(cfg.Commands[string(s)]) != "" {
				continue
			}
			out = append(out, FloorViolation{
				Floor: FloorCommands, Field: "commands." + string(s),
				Message: fmt.Sprintf("is not set; the profile requires step %s, which runs this command", s),
			})
		}
	}
	return out
}

// needsCommand reports whether a step only runs when commands: names a command
// for it. credentials and rebase are native; agent steps run an agent.
func needsCommand(s StepName) bool {
	return s != StepCredentials && s != StepRebase && s != StepPush && !s.IsAgentStep()
}

func checkContainer(cfg Config) []FloorViolation {
	var out []FloorViolation
	if cfg.Execution.Mode == "host" {
		out = append(out, FloorViolation{
			Floor: FloorContainer, Field: "execution.mode",
			Message: "is host; shell checks must run in a container",
			SetBy:   func(c Config) bool { return c.Execution.Mode == "host" },
		})
	}
	if cfg.Execution.Image == "" {
		out = append(out, FloorViolation{
			Floor: FloorContainer, Field: "execution.image",
			Message: "is not set; shell checks must run in a container",
		})
	}
	return out
}

func checkNetwork(cfg Config) []FloorViolation {
	if cfg.Execution.Network == "none" {
		return nil
	}
	got := cfg.Execution.Network
	return []FloorViolation{{
		Floor: FloorNetworkNone, Field: "execution.network",
		Message: fmt.Sprintf("is %q; it must be none", got),
		SetBy:   func(c Config) bool { return c.Execution.Network == got },
	}}
}

func checkAgentWrites(cfg Config) []FloorViolation {
	var out []FloorViolation
	agents := agentSteps(cfg)
	for i, r := range cfg.Rules {
		for _, s := range sortedStepKeys(r.Then.AutoFix) {
			if r.Then.AutoFix[s] <= 0 || !slices.Contains(agents, s) {
				continue
			}
			out = append(out, FloorViolation{
				Floor: FloorNoAgentWrites, Field: fmt.Sprintf("rules[%d].then.auto_fix.%s", i, s),
				Message: fmt.Sprintf("is set, and %s is an agent step; agent auto-fix is not allowed", s),
				SetBy: func(c Config) bool {
					for _, r := range c.Rules {
						if r.Then.AutoFix[s] > 0 {
							return true
						}
					}
					return false
				},
			})
		}
	}
	for _, w := range cfg.Writes {
		s := StepName(w)
		if !slices.Contains(agents, s) {
			continue
		}
		out = append(out, FloorViolation{
			Floor: FloorNoAgentWrites, Field: "writes",
			Message: fmt.Sprintf("lists agent step %s; kept agent writes are not allowed", s),
			SetBy:   func(c Config) bool { return slices.Contains(c.Writes, w) },
		})
	}
	return out
}

func checkAgentTimeouts(cfg Config) []FloorViolation {
	var out []FloorViolation
	for _, s := range agentSteps(cfg) {
		v, ok := cfg.Timeouts[string(s)]
		if !ok {
			continue // Apply fills the default; Check on a raw config skips it
		}
		d, err := time.ParseDuration(v)
		if err != nil || d > 0 {
			continue
		}
		out = append(out, FloorViolation{
			Floor: FloorAgentTimeout, Field: "timeouts." + string(s),
			Message: fmt.Sprintf("is %q (no limit); agent steps need a timeout", v),
			SetBy: func(c Config) bool {
				got, ok := c.Timeouts[string(s)]
				return ok && got == v
			},
		})
	}
	return out
}

// agentSteps lists the steps an agent can run under cfg: the built-in agent
// steps, plus any step a rule hands to an agent. Which rules match is only known
// per invocation, so a step any rule assigns an agent counts; that errs toward
// treating a step as an agent step, which only tightens.
func agentSteps(cfg Config) []StepName {
	out := []StepName{StepIntent, StepReview, StepDocument}
	for _, r := range cfg.Rules {
		for _, s := range sortedStepKeys(r.Then.Agent) {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func sortedStepKeys[V any](m map[StepName]V) []StepName {
	out := make([]StepName, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// EffectiveHookSteps returns the step list a hook starts from before rules
// apply: the configured list, or the built-in default when config omits it.
// It mirrors the policy resolver's base list, so a floor reads what a run would.
func EffectiveHookSteps(cfg Config, h Hook) []StepName {
	if s, ok := cfg.Steps[h.ConfigKey()]; ok {
		return slices.Clone(s)
	}
	return DefaultSteps(h)
}

// mergeProfile ratchets the profile across extends: the higher of base and
// child wins. A child naming a lower profile is refused by the loader, which
// can name both files; here, with no files to name, the higher one is kept so
// the merge can never lower the floor. An unparseable value cannot reach this
// far (Validate rejects it per file), but if one does, the child's is kept so
// the loader's later check fails on it rather than it disappearing.
func mergeProfile(base, child string) string {
	if child == "" {
		return base
	}
	if base == "" {
		return child
	}
	b, errB := ParseComplianceProfile(base)
	c, errC := ParseComplianceProfile(child)
	if errB != nil || errC != nil {
		return child
	}
	if c.Lowers(b) {
		return base
	}
	return child
}
