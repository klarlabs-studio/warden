package domain

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// WorkspaceEcosystems are the ecosystem labels workspace discovery knows:
// "js" (npm, yarn and pnpm workspaces), "go" (go.work multi-module repos) and
// "rust" (Cargo workspaces).
// They are the values `workspaces.discover` and `per_workspace.<step>.ecosystems`
// accept, and the label a discovered Workspace carries.
var WorkspaceEcosystems = []string{"js", "go", "rust"}

// WorkspaceSelectMode says which workspaces a per-workspace step runs for.
type WorkspaceSelectMode string

const (
	// WorkspaceSelectAll runs every per-workspace step for every workspace. It
	// is the default (also for "").
	WorkspaceSelectAll WorkspaceSelectMode = "all"
	// WorkspaceSelectAffected runs it only for the workspaces Select returns.
	WorkspaceSelectAffected WorkspaceSelectMode = "affected"
)

// Placeholders a per-workspace command may use. Each is replaced with a
// shell-quoted value when the command runs: a workspace's name and path come
// from manifests, which the author of a pull request controls.
//
// The singular ones name the one workspace an `each:` command runs for; the
// plural ones name every selected workspace for the single `batch:` call. A
// placeholder outside its own mode, or anywhere else in the config, is refused:
// nothing would replace it, so the command would run with the literal text.
const (
	PlaceholderWorkspacePath   = "{workspace.path}"
	PlaceholderWorkspaceName   = "{workspace.name}"
	PlaceholderWorkspacesPaths = "{workspaces.paths}"
	PlaceholderWorkspacesNames = "{workspaces.names}"
	// PlaceholderWorkspacesFilter is the selection in the form a monorepo tool
	// takes as a filter argument.
	PlaceholderWorkspacesFilter = "{workspaces.filter}"
)

var (
	eachPlaceholders  = []string{PlaceholderWorkspacePath, PlaceholderWorkspaceName}
	batchPlaceholders = []string{PlaceholderWorkspacesPaths, PlaceholderWorkspacesNames, PlaceholderWorkspacesFilter}
)

// workspacePlaceholderRe finds anything written as a workspace placeholder,
// known or not, so a typo ({workspace.dir}) is refused instead of reaching the
// shell as literal text.
var workspacePlaceholderRe = regexp.MustCompile(`\{workspaces?\.[^{}\s]*\}`)

// WorkspacesConfig is the `workspaces:` block: how warden finds a monorepo's
// workspaces and what a change to them means (design 0006).
//
// Every field is omitempty, in both encodings, for the reason Profile and
// Diagnostics are: Save must not write keys an older warden rejects, and the
// configuration digest of a repo that does not use the block is part of signed
// execution contracts, so it must not change.
type WorkspacesConfig struct {
	// Discover names the ecosystems whose workspaces are read (WorkspaceEcosystems).
	// It is explicit: nothing is discovered unless it is named here.
	Discover []string `yaml:"discover,omitempty" json:"Discover,omitempty"`
	// Select is "all" (the default) or "affected".
	Select WorkspaceSelectMode `yaml:"select,omitempty" json:"Select,omitempty"`
	// Global globs force a full selection when a changed path matches. They
	// add to DefaultGlobalPaths.
	Global []string `yaml:"global,omitempty" json:"Global,omitempty"`
	// Ignore globs exempt changed paths that belong to no workspace.
	Ignore []string `yaml:"ignore,omitempty" json:"Ignore,omitempty"`
	// Unowned is "full" (the default) or "ignore": what a changed path that
	// belongs to no workspace and matches no Ignore glob does.
	Unowned UnownedMode `yaml:"unowned,omitempty" json:"Unowned,omitempty"`
	// Concurrency bounds how many workspaces run at once across every
	// per-workspace step. Zero takes the run-time default.
	Concurrency int `yaml:"concurrency,omitempty" json:"Concurrency,omitempty"`
}

// PerWorkspaceStep defines a step that runs per workspace: exactly one of Each
// (once per selected workspace, in its directory) or Batch (one call naming
// every selected workspace).
type PerWorkspaceStep struct {
	Each  string `yaml:"each,omitempty" json:"Each,omitempty"`
	Batch string `yaml:"batch,omitempty" json:"Batch,omitempty"`
	// Ecosystems limits the step to workspaces of these ecosystems. Empty
	// means every discovered workspace.
	Ecosystems []string `yaml:"ecosystems,omitempty" json:"Ecosystems,omitempty"`
}

// Mode is "each" or "batch", whichever the step defines.
func (s PerWorkspaceStep) Mode() string {
	if s.Batch != "" {
		return "batch"
	}
	return "each"
}

// Command is the step's command template.
func (s PerWorkspaceStep) Command() string {
	if s.Batch != "" {
		return s.Batch
	}
	return s.Each
}

// validateWorkspaces checks the workspaces and per_workspace blocks of one
// config file. Rules that depend on what other files in the extends chain set
// are in ValidateEffective.
func (c Config) validateWorkspaces() error {
	if w := c.Workspaces; w != nil {
		if err := w.validate(); err != nil {
			return err
		}
	}
	for _, step := range sortedKeys(c.PerWorkspace) {
		if err := validatePerWorkspaceStep(step, c.PerWorkspace[step]); err != nil {
			return err
		}
		// A step has one definition. Allowing both would leave which one runs
		// to an implementation detail, and the repo-wide one would silently
		// stop running once per-workspace execution arrives.
		if _, ok := c.Commands[step]; ok {
			return fmt.Errorf("step %q is defined under both commands: and per_workspace:; a step runs either repo-wide or per workspace, so remove one", step)
		}
	}
	return validatePlaceholdersAbsent(c)
}

func (w WorkspacesConfig) validate() error {
	if err := validateEcosystems("workspaces.discover", w.Discover); err != nil {
		return err
	}
	switch w.Select {
	case "", WorkspaceSelectAll, WorkspaceSelectAffected:
	default:
		return fmt.Errorf("workspaces.select must be %q or %q, not %q", WorkspaceSelectAll, WorkspaceSelectAffected, w.Select)
	}
	if w.Concurrency < 0 {
		return fmt.Errorf("workspaces.concurrency must not be negative (0 takes the default), got %d", w.Concurrency)
	}
	return w.selectionRules().Validate()
}

func (w WorkspacesConfig) selectionRules() SelectionRules {
	return SelectionRules{Global: w.Global, Ignore: w.Ignore, Unowned: w.Unowned}
}

// SelectionRules returns the rules Select reads from this config's
// workspaces block: its global and ignore globs and its unowned mode, with
// the default mode spelled out. A config without the block yields the
// defaults.
func (c Config) SelectionRules() SelectionRules {
	var r SelectionRules
	if c.Workspaces != nil {
		r = c.Workspaces.selectionRules()
	}
	if r.Unowned == "" {
		r.Unowned = UnownedFull
	}
	return r
}

func validateEcosystems(key string, values []string) error {
	seen := map[string]bool{}
	for _, e := range values {
		if !slices.Contains(WorkspaceEcosystems, e) {
			return fmt.Errorf("%s: unknown ecosystem %q (known: %s)", key, e, strings.Join(WorkspaceEcosystems, ", "))
		}
		if seen[e] {
			return fmt.Errorf("%s: %q is listed twice", key, e)
		}
		seen[e] = true
	}
	return nil
}

func validatePerWorkspaceStep(step string, s PerWorkspaceStep) error {
	name := StepName(step)
	if !name.Valid() {
		return fmt.Errorf("invalid step name %q in per_workspace", step)
	}
	// Only shell checks can fan out. A built-in step other than lint and test
	// runs warden's own code, and security-scan reads the scanner's report for
	// the whole tree: a scan of some workspaces would be recorded as a scan.
	if name == StepSecurityScan || (name.IsBuiltin() && name != StepLint && name != StepTest) {
		return fmt.Errorf("invalid per_workspace.%s: only shell checks (lint, test, or a custom step) can run per workspace", step)
	}
	each, batch := strings.TrimSpace(s.Each) != "", strings.TrimSpace(s.Batch) != ""
	switch {
	case each && batch:
		return fmt.Errorf("per_workspace.%s: set either each: or batch:, not both", step)
	case !each && !batch:
		return fmt.Errorf("per_workspace.%s: set each: (once per workspace) or batch: (one call for all of them)", step)
	}
	if err := validateEcosystems(fmt.Sprintf("per_workspace.%s.ecosystems", step), s.Ecosystems); err != nil {
		return err
	}
	allowed, other := eachPlaceholders, "a batch:"
	if batch {
		allowed, other = batchPlaceholders, "an each:"
	}
	for _, ph := range workspacePlaceholderRe.FindAllString(s.Command(), -1) {
		if slices.Contains(allowed, ph) {
			continue
		}
		if slices.Contains(eachPlaceholders, ph) || slices.Contains(batchPlaceholders, ph) {
			return fmt.Errorf("per_workspace.%s.%s: %s belongs in %s command; this one can use %s", step, s.Mode(), ph, other, strings.Join(allowed, ", "))
		}
		return fmt.Errorf("per_workspace.%s.%s: unknown placeholder %s; this command can use %s", step, s.Mode(), ph, strings.Join(allowed, ", "))
	}
	return nil
}

// validatePlaceholdersAbsent refuses a workspace placeholder in a repo-wide
// command. Nothing replaces it there, so the shell would receive the literal
// text and the command would quietly check something other than intended.
func validatePlaceholdersAbsent(c Config) error {
	for _, block := range []struct {
		key string
		m   map[string]string
	}{{"commands", c.Commands}, {"agent_commands", c.AgentCommands}} {
		for _, k := range sortedKeys(block.m) {
			if ph := workspacePlaceholderRe.FindString(block.m[k]); ph != "" {
				return fmt.Errorf("%s.%s: %s is only replaced in a per_workspace: command; define the step there instead", block.key, k, ph)
			}
		}
	}
	return nil
}

// ValidateEffective checks rules that hold across the whole extends chain,
// which no single file can be checked for: the ecosystems to discover and the
// per_workspace steps that run over them may each be declared in a base. The loader calls
// it on the merged config.
func (c Config) ValidateEffective() error {
	if c.Workspaces != nil && len(c.Workspaces.Discover) == 0 {
		return fmt.Errorf("workspaces.discover: name the ecosystems to discover (%s); nothing is discovered implicitly", strings.Join(WorkspaceEcosystems, ", "))
	}
	if len(c.PerWorkspace) > 0 && (c.Workspaces == nil || len(c.Workspaces.Discover) == 0) {
		return fmt.Errorf("per_workspace: steps need workspaces to run over; set workspaces.discover (%s)", strings.Join(WorkspaceEcosystems, ", "))
	}
	return c.validatePerWorkspaceUnsupported()
}

// validatePerWorkspaceUnsupported refuses settings a per-workspace step would
// silently ignore. Each of them is read only for a repo-wide command step, so
// accepting one would look like configuration and do nothing: a cache: entry
// that never skips, a predictive_cache: entry that never predicts, a SARIF
// file that is never read. They are checked on the merged config because the
// setting and the per_workspace: definition may come from different files.
func (c Config) validatePerWorkspaceUnsupported() error {
	for _, step := range sortedKeys(c.PerWorkspace) {
		if _, ok := c.Cache[step]; ok {
			return fmt.Errorf("cache.%s: a per_workspace: step is not cached; remove the entry (each run checks the selected workspaces)", step)
		}
		if slices.Contains(c.PredictiveCache, step) {
			return fmt.Errorf("predictive_cache: %s is a per_workspace: step, which never reuses a staged prediction; remove it from the list", step)
		}
		if _, ok := c.Diagnostics[step]; ok {
			return fmt.Errorf("diagnostics.%s: a per_workspace: step does not read a SARIF file; remove the entry", step)
		}
	}
	return nil
}

// mergeWorkspaces overlays a child workspaces block field by field. Global
// unions: like writes:, it is a safety declaration ("a change here can affect
// every workspace"), so a child must not silently drop a path its base marked.
// The other lists replace the base's when the child sets them.
func mergeWorkspaces(base, child *WorkspacesConfig) *WorkspacesConfig {
	if base == nil && child == nil {
		return nil
	}
	var out WorkspacesConfig
	if base != nil {
		out = *base
		out.Discover = slices.Clone(base.Discover)
		out.Global = slices.Clone(base.Global)
		out.Ignore = slices.Clone(base.Ignore)
	}
	if child == nil {
		return &out
	}
	if len(child.Discover) > 0 {
		out.Discover = slices.Clone(child.Discover)
	}
	if child.Select != "" {
		out.Select = child.Select
	}
	out.Global = unionStrings(out.Global, child.Global)
	if len(child.Ignore) > 0 {
		out.Ignore = slices.Clone(child.Ignore)
	}
	if child.Unowned != "" {
		out.Unowned = child.Unowned
	}
	if child.Concurrency != 0 {
		out.Concurrency = child.Concurrency
	}
	return &out
}

// mergeStepDefinitions overlays the child's commands: and per_workspace: onto
// the base's, per key, child wins. A step has one definition, so the child's
// wins whichever block it is in: a child that moves `test` from commands: to
// per_workspace: (or back) replaces the base's definition instead of ending up
// with both.
func mergeStepDefinitions(base, child Config) (commands map[string]string, perWorkspace map[string]PerWorkspaceStep) {
	commands = mergeStringMap(base.Commands, child.Commands)
	perWorkspace = mergePerWorkspace(base.PerWorkspace, child.PerWorkspace)
	for step := range child.PerWorkspace {
		if _, own := child.Commands[step]; !own {
			delete(commands, step)
		}
	}
	for step := range child.Commands {
		if _, own := child.PerWorkspace[step]; !own {
			delete(perWorkspace, step)
		}
	}
	if len(perWorkspace) == 0 {
		perWorkspace = nil
	}
	return commands, perWorkspace
}

func mergePerWorkspace(base, child map[string]PerWorkspaceStep) map[string]PerWorkspaceStep {
	if base == nil && child == nil {
		return nil
	}
	out := make(map[string]PerWorkspaceStep, len(base)+len(child))
	maps.Copy(out, base)
	maps.Copy(out, child)
	return out
}

// ResolvedWorkspaces is the workspace configuration a policy was resolved
// under, for `policy explain` and for the run that will fan steps out.
type ResolvedWorkspaces struct {
	Discover []string
	// Select is the effective mode, "all" when the config leaves it unset.
	Select WorkspaceSelectMode
	// Rules are the configured inputs Select reads besides the graphs and the
	// changed paths. DefaultGlobalPaths are not included: Select adds them.
	Rules SelectionRules
	// Concurrency is the configured bound; 0 takes the run-time default.
	Concurrency int
	// Steps maps each per-workspace step to its definition.
	Steps map[StepName]PerWorkspaceStep
	// Active reports whether this warden runs per-workspace steps. It is
	// true from the release that runs them; Notice says what runs where for
	// the hook the policy was resolved for (WorkspaceExecutionNotice).
	Active bool
	Notice string
}

// SelectionInput returns the input to Select for the given graphs, changed
// paths and configuration digests, with the configured rules.
func (w ResolvedWorkspaces) SelectionInput(base, head WorkspaceGraph, changed []string, anchorDigest, digest string) SelectionInput {
	return SelectionInput{
		Base: base, Head: head, Changed: changed, Rules: w.Rules,
		AnchorConfigurationDigest: anchorDigest, ConfigurationDigest: digest,
	}
}
