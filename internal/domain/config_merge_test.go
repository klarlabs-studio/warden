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
