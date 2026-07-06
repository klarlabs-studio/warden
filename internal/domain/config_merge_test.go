package domain

import (
	"reflect"
	"testing"
)

func TestOverlayOnto(t *testing.T) {
	base := Config{
		Agent:    "claude",
		Commands: map[string]string{"lint": "golangci-lint run", "test": "go test ./..."},
		Timeouts: map[string]string{"test": "5m"},
		Steps:    map[string][]StepName{"pre_push": {"lint", "test"}},
		Rules:    []Rule{{Match: Match{Branch: "main"}}},
	}
	child := Config{
		Commands: map[string]string{"test": "go test -race ./..."}, // override one key
		Cache:    map[string][]string{"test": {"**/*.go"}},         // add a section
		Rules:    []Rule{{Match: Match{Risk: RiskHigh}}},           // append a rule
		Extends:  "../base.yaml",
	}

	got := child.OverlayOnto(base)

	if got.Agent != "claude" {
		t.Errorf("inherited agent lost: %q", got.Agent)
	}
	if got.Commands["lint"] != "golangci-lint run" {
		t.Error("base-only command not inherited")
	}
	if got.Commands["test"] != "go test -race ./..." {
		t.Errorf("child override lost: %q", got.Commands["test"])
	}
	if got.Timeouts["test"] != "5m" {
		t.Error("base timeout not inherited")
	}
	if !reflect.DeepEqual(got.Cache["test"], []string{"**/*.go"}) {
		t.Error("child-only cache section lost")
	}
	if len(got.Rules) != 2 || got.Rules[0].Match.Branch != "main" || got.Rules[1].Match.Risk != RiskHigh {
		t.Errorf("rules must stack base-then-child: %+v", got.Rules)
	}
	if got.Extends != "" {
		t.Error("merged config must not still extend")
	}
}

func TestOverlayOnto_ChildScalarsWin(t *testing.T) {
	no := false
	base := Config{Parallel: nil, Notify: nil, Risk: RiskConfig{DiffLinesHigh: 100}}
	child := Config{Parallel: &no, Notify: &no, Risk: RiskConfig{DiffLinesHigh: 500}}
	got := child.OverlayOnto(base)
	if got.Parallel == nil || *got.Parallel {
		t.Error("child parallel=false must win")
	}
	if got.Risk.DiffLinesHigh != 500 {
		t.Errorf("child risk must win: %d", got.Risk.DiffLinesHigh)
	}
}

// TestOverlayOnto_StepsUnionKeepsBaseStep guards the security fix: a repo that
// re-declares a hook's steps must not silently drop an org-mandated base step.
func TestOverlayOnto_StepsUnionKeepsBaseStep(t *testing.T) {
	base := Config{Steps: map[string][]StepName{"pre_push": {"review", "test", "lint"}}}
	// Child omits the org-required "review" step and adds a repo-specific one.
	child := Config{Steps: map[string][]StepName{"pre_push": {"test", "lint", "bench"}}}
	got := child.OverlayOnto(base)
	want := []StepName{"review", "test", "lint", "bench"}
	if !reflect.DeepEqual(got.Steps["pre_push"], want) {
		t.Errorf("steps union = %v, want %v (base 'review' must survive)", got.Steps["pre_push"], want)
	}
}

// TestOverlayOnto_WritesUnion guards that a child cannot silently drop a base's
// `writes:` declaration — a step the base marked tree-mutating must stay a
// barrier even if the child re-declares its own writers.
func TestOverlayOnto_WritesUnion(t *testing.T) {
	base := Config{Writes: []string{"codegen"}}
	child := Config{Writes: []string{"format"}}
	got := child.OverlayOnto(base)
	want := []string{"codegen", "format"}
	if !reflect.DeepEqual(got.Writes, want) {
		t.Errorf("writes union = %v, want %v (base 'codegen' must survive)", got.Writes, want)
	}
}

// TestOverlayOnto_RiskFieldLevel guards that a child setting only one threshold
// does not zero the base's other threshold.
func TestOverlayOnto_RiskFieldLevel(t *testing.T) {
	base := Config{Risk: RiskConfig{DiffLinesHigh: 100, FilesTouchedHigh: 20}}
	child := Config{Risk: RiskConfig{FilesTouchedHigh: 30}} // only sets one field
	got := child.OverlayOnto(base)
	if got.Risk.DiffLinesHigh != 100 {
		t.Errorf("base DiffLinesHigh must survive a partial child risk: %d", got.Risk.DiffLinesHigh)
	}
	if got.Risk.FilesTouchedHigh != 30 {
		t.Errorf("child FilesTouchedHigh must win: %d", got.Risk.FilesTouchedHigh)
	}
}

func TestStepNameValid(t *testing.T) {
	valid := []StepName{StepIntent, StepRebase, StepReview, StepTest, StepDocument, StepLint, StepPush, "security-scan", "bench_2", "a"}
	for _, n := range valid {
		if !n.Valid() {
			t.Errorf("step name %q should be valid", n)
		}
	}
	invalid := []StepName{"x/evil", "../../bin/sh", "a b", "bad;rm", "-leading", "", "a.b", "$(x)"}
	for _, n := range invalid {
		if n.Valid() {
			t.Errorf("step name %q should be rejected", n)
		}
	}
}

func TestConfigValidate_RejectsUnsafeStepNames(t *testing.T) {
	cases := map[string]Config{
		"steps":                  {Steps: map[string][]StepName{"pre_push": {"lint", "x/evil"}}},
		"rule-add":               {Rules: []Rule{{Then: Then{Steps: map[string]StepEdit{"pre_push": {Add: []StepName{"../evil"}}}}}}},
		"rule-autofix":           {Rules: []Rule{{Then: Then{AutoFix: map[StepName]int{"a;b": 1}}}}},
		"notify_after-nounit":    {NotifyAfter: "10"},
		"notify_after-garbage":   {NotifyAfter: "soon"},
		"notify_after-negative":  {NotifyAfter: "-5s"},
		"notify_after-typo-unit": {NotifyAfter: "10ss"},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	ok := Config{
		Steps:       map[string][]StepName{"pre_push": {"intent", "review", "security-scan"}},
		Rules:       []Rule{{Then: Then{AutoFix: map[StepName]int{"lint": 1}, Agent: map[StepName]string{"review": "codex"}}}},
		NotifyAfter: "45s",
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
