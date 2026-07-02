// Package policy is Warden's rule-resolution domain service. It resolves a
// domain Config into a ResolvedPolicy for a concrete hook invocation, applying
// rule matching and stacking (§5.2). It is a pure domain service: it depends
// only on the domain model and the standard library — no I/O, no infrastructure
// — so config loading and persistence live in infrastructure/config instead.
package policy

import (
	"maps"
	"sort"

	"go.klarlabs.de/warden/internal/domain"
)

// Input carries everything a resolution needs about the invocation under
// evaluation.
type Input struct {
	Hook   domain.Hook
	Branch string
	Paths  []string
	Risk   domain.Risk
}

// matched is a rule that fired, tagged with its declaration index and
// specificity so per-field overlay can pick a winner.
type matched struct {
	rule        domain.Rule
	index       int
	specificity int
	name        string
}

// Resolve computes the effective policy for the invocation. Matching rules
// stack: per field the most specific matching rule wins, ties broken by
// declaration order (later wins); step add/skip are unioned across all
// matching rules (§5.2).
func Resolve(cfg domain.Config, in Input) domain.ResolvedPolicy {
	matches := matchingRules(cfg, in)

	res := domain.ResolvedPolicy{
		Hook:     in.Hook,
		Agents:   map[domain.StepName]string{},
		AutoFix:  map[domain.StepName]int{},
		Commands: cfg.Commands,
		Risk:     in.Risk,
		// Concurrent step execution is on unless explicitly disabled.
		Parallel: cfg.Parallel == nil || *cfg.Parallel,
	}

	res.Steps = resolveSteps(cfg, in.Hook, matches)
	resolveOverlays(&res, matches)

	for _, m := range matches {
		res.MatchedRules = append(res.MatchedRules, m.name)
	}
	return res
}

// matchingRules returns the rules whose every set condition is satisfied, in
// declaration order, each annotated with specificity.
func matchingRules(cfg domain.Config, in Input) []matched {
	var out []matched
	for i, r := range cfg.Rules {
		if !ruleMatches(r.Match, in) {
			continue
		}
		out = append(out, matched{
			rule:        r,
			index:       i,
			specificity: r.Match.Specificity(),
			name:        ruleName(r, i),
		})
	}
	return out
}

// ruleMatches reports whether every set field of m holds for the invocation.
func ruleMatches(m domain.Match, in Input) bool {
	if m.Branch != "" && !globMatch(m.Branch, in.Branch) {
		return false
	}
	if m.Risk != "" && m.Risk != in.Risk {
		return false
	}
	if len(m.Paths) > 0 && !anyPathMatches(m.Paths, in.Paths) {
		return false
	}
	return true
}

// anyPathMatches is true if any glob matches any changed path (glob-any, §5.2).
func anyPathMatches(globs, paths []string) bool {
	for _, g := range globs {
		for _, p := range paths {
			if globMatch(g, p) {
				return true
			}
		}
	}
	return false
}

// resolveSteps starts from the base step list for the hook (config-supplied or
// default) and applies unioned add/skip directives from all matching rules.
func resolveSteps(cfg domain.Config, hook domain.Hook, matches []matched) []domain.StepName {
	base := baseSteps(cfg, hook)

	// Collect unioned skip and positioned adds across all matching rules.
	skip := map[domain.StepName]bool{}
	type add struct {
		step  domain.StepName
		after domain.StepName
	}
	var adds []add
	seenAdd := map[domain.StepName]bool{}

	key := hook.ConfigKey()
	for _, m := range matches {
		edit, ok := m.rule.Then.Steps[key]
		if !ok {
			continue
		}
		for _, s := range edit.Skip {
			skip[s] = true
		}
		for _, s := range edit.Add {
			if seenAdd[s] {
				continue
			}
			seenAdd[s] = true
			adds = append(adds, add{step: s, after: edit.InsertAfter})
		}
	}

	// Build result: base minus skips, then insert adds.
	var steps []domain.StepName
	for _, s := range base {
		if !skip[s] {
			steps = append(steps, s)
		}
	}
	for _, a := range adds {
		if skip[a.step] {
			continue
		}
		steps = insertAfter(steps, a.step, a.after)
	}
	return steps
}

// baseSteps returns the hook's configured step list, or the built-in default
// when config omits it.
func baseSteps(cfg domain.Config, hook domain.Hook) []domain.StepName {
	if cfg.Steps != nil {
		if s, ok := cfg.Steps[hook.ConfigKey()]; ok {
			return append([]domain.StepName(nil), s...)
		}
	}
	return domain.DefaultSteps(hook)
}

// insertAfter places step immediately after the named anchor; if the anchor is
// empty or absent, step is appended.
func insertAfter(steps []domain.StepName, step, after domain.StepName) []domain.StepName {
	if after == "" {
		return append(steps, step)
	}
	for i, s := range steps {
		if s != after {
			continue
		}
		out := make([]domain.StepName, 0, len(steps)+1)
		out = append(out, steps[:i+1]...)
		out = append(out, step)
		out = append(out, steps[i+1:]...)
		return out
	}
	return append(steps, step)
}

// resolveOverlays applies per-field last-most-specific-wins overlays for
// agent, auto_fix, and require_approval (§5.2).
func resolveOverlays(res *domain.ResolvedPolicy, matches []matched) {
	// Order matches by (specificity asc, index asc) so that later, more
	// specific writes overwrite earlier ones — leaving the winner in place.
	ordered := append([]matched(nil), matches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].specificity != ordered[j].specificity {
			return ordered[i].specificity < ordered[j].specificity
		}
		return ordered[i].index < ordered[j].index
	})

	for _, m := range ordered {
		t := m.rule.Then
		maps.Copy(res.Agents, t.Agent)
		maps.Copy(res.AutoFix, t.AutoFix)
		if t.RequireApproval != nil {
			res.RequireApproval = *t.RequireApproval
		}
	}
}
