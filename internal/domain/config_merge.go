package domain

import "maps"

// OverlayOnto returns base with this config layered on top: every field the
// child (c) sets wins; anything it leaves unset inherits from base. Maps merge
// per key (child keys win); rules concatenate (base first, then child) so an
// org base can set broad policy and a repo append stricter rules. This backs
// `extends:` — the org-policy-sync mechanism (§5.2).
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

	if c.Risk != (RiskConfig{}) {
		out.Risk = c.Risk
	}
	if c.PR != (PRConfig{}) {
		out.PR = c.PR
	}
	if c.Parallel != nil {
		out.Parallel = c.Parallel
	}
	if c.Notify != nil {
		out.Notify = c.Notify
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

func mergeStepMap(base, child map[string][]StepName) map[string][]StepName {
	if base == nil && child == nil {
		return nil
	}
	out := make(map[string][]StepName, len(base)+len(child))
	maps.Copy(out, base)
	maps.Copy(out, child)
	return out
}
