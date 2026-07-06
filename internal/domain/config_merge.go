package domain

import "maps"

// OverlayOnto returns base with this config layered on top: every field the
// child (c) sets wins; anything it leaves unset inherits from base. Command/
// timeout/cache maps merge per key (child keys win); risk thresholds merge
// per field; the per-hook `steps:` lists UNION (base steps are preserved so a
// repo cannot silently drop an org-mandated step — see mergeStepMap); rules
// concatenate (base first, then child) so an org base can set broad policy and
// a repo append stricter rules. This backs `extends:` — the org-policy-sync
// mechanism (§5.2).
func (c Config) OverlayOnto(base Config) Config {
	out := base

	if c.Agent != "" {
		out.Agent = c.Agent
	}
	// Hooks: a child that names any hook selection replaces the base's (the
	// installed shims are a repo-local fact, not something to inherit-merge).
	if c.Hooks != (HookConfig{}) {
		out.Hooks = c.Hooks
	}
	out.Commands = mergeStringMap(base.Commands, c.Commands)
	out.AgentCommands = mergeStringMap(base.AgentCommands, c.AgentCommands)
	out.Timeouts = mergeStringMap(base.Timeouts, c.Timeouts)
	out.Cache = mergeGlobMap(base.Cache, c.Cache)
	out.Steps = mergeStepMap(base.Steps, c.Steps)
	// MaterializeDeps: a child that names any step replaces the base's list (it is
	// a repo-local performance choice, not something to inherit-merge).
	if len(c.MaterializeDeps) > 0 {
		out.MaterializeDeps = c.MaterializeDeps
	}
	// Writes unions: it is a safety declaration ("this step mutates the tree"), so
	// a child must not be able to silently drop a base's writer marking and let a
	// tree-mutating step race a concurrent check.
	out.Writes = unionStrings(base.Writes, c.Writes)

	// Risk merges field-by-field: a child that sets only one threshold must not
	// discard the base's other threshold (a whole-struct replace would zero the
	// sibling field and silently reset it to the built-in default).
	out.Risk = mergeRisk(base.Risk, c.Risk)
	if c.PR != (PRConfig{}) {
		out.PR = c.PR
	}
	if c.Parallel != nil {
		out.Parallel = c.Parallel
	}
	if c.SymlinkDeps != nil {
		out.SymlinkDeps = c.SymlinkDeps
	}
	if c.Notify != nil {
		out.Notify = c.Notify
	}
	if c.NotifyAfter != "" {
		out.NotifyAfter = c.NotifyAfter
	}
	// Rules stack: base first (broad), then the child's (repo-specific).
	if len(c.Rules) > 0 {
		out.Rules = append(append([]Rule(nil), base.Rules...), c.Rules...)
	}
	// Extends is resolved by the loader; the merged result no longer extends.
	out.Extends = ""
	return out
}

func mergeStringMap(base, child map[string]string) map[string]string {
	if base == nil && child == nil {
		return nil
	}
	out := make(map[string]string, len(base)+len(child))
	maps.Copy(out, base)
	maps.Copy(out, child)
	return out
}

func mergeGlobMap(base, child map[string][]string) map[string][]string {
	if base == nil && child == nil {
		return nil
	}
	out := make(map[string][]string, len(base)+len(child))
	maps.Copy(out, base)
	maps.Copy(out, child)
	return out
}

// mergeStepMap unions the per-hook step lists instead of letting the child
// replace a hook wholesale. This backs `extends:`: an org base that lists a
// mandated step (e.g. a security review in pre_push) must not be silently
// dropped by a repo that re-declares `steps.pre_push` — a whole-list replace
// would neuter org policy while `policy explain`/provenance still advertised
// the base list. The union preserves every base step (in base order) and
// appends any additional child steps the base did not already name. A repo can
// still add steps and, for genuine per-run exceptions, skip one explicitly via
// a rule's `steps.skip` — which is auditable — but it can no longer erase a
// base step just by omitting it from its own list.
func mergeStepMap(base, child map[string][]StepName) map[string][]StepName {
	if base == nil && child == nil {
		return nil
	}
	out := make(map[string][]StepName, len(base)+len(child))
	for hook, names := range base {
		out[hook] = append([]StepName(nil), names...)
	}
	for hook, childNames := range child {
		out[hook] = unionSteps(out[hook], childNames)
	}
	return out
}

// unionSteps returns base followed by every child step not already present,
// preserving order and dropping duplicates.
func unionSteps(base, child []StepName) []StepName {
	seen := make(map[StepName]bool, len(base)+len(child))
	out := make([]StepName, 0, len(base)+len(child))
	for _, n := range base {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range child {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// unionStrings returns base followed by every child entry not already present,
// preserving order and dropping duplicates.
func unionStrings(base, child []string) []string {
	if len(base) == 0 && len(child) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(base)+len(child))
	out := make([]string, 0, len(base)+len(child))
	for _, n := range append(append([]string(nil), base...), child...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// mergeRisk overlays child risk thresholds onto base field-by-field: a field
// the child leaves zero inherits the base value rather than resetting it.
func mergeRisk(base, child RiskConfig) RiskConfig {
	out := base
	if child.DiffLinesHigh != 0 {
		out.DiffLinesHigh = child.DiffLinesHigh
	}
	if child.FilesTouchedHigh != 0 {
		out.FilesTouchedHigh = child.FilesTouchedHigh
	}
	return out
}
