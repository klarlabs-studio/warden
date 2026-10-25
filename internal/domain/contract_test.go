package domain

import (
	"strings"
	"testing"
)

func TestExecutionContractRequirements(t *testing.T) {
	digest := DigestBytes([]byte("input"))
	env := ExecutionEnvironment{Runtime: "docker", ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	requirement := StepRequirements{Command: "go test ./...", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	contract := ExecutionContract{Version: 1, CommandDigest: DigestBytes([]byte(requirement.Command)), ConfigurationDigest: digest, ContextDigest: digest, TreeSHA: strings.Repeat("a", 40), Inputs: map[string]string{}, Environment: env}
	requirements := ExecutionRequirements{Version: 1, Steps: map[StepName]StepRequirements{StepTest: requirement}}
	if err := requirements.Validate(); err != nil {
		t.Fatal(err)
	}
	if reason := requirement.Mismatch(contract, contract.TreeSHA); reason != "" {
		t.Fatal(reason)
	}
	tests := []struct {
		name   string
		change func(*ExecutionContract)
	}{
		{"command", func(c *ExecutionContract) { c.CommandDigest = "" }},
		{"configuration", func(c *ExecutionContract) { c.ConfigurationDigest = "" }},
		{"context", func(c *ExecutionContract) { c.ContextDigest = "" }},
		{"tree", func(c *ExecutionContract) { c.TreeSHA = strings.Repeat("b", 40) }},
		{"inputs", func(c *ExecutionContract) { c.Inputs = nil }},
		{"image", func(c *ExecutionContract) { c.Environment.ImageID = "" }},
		{"host", func(c *ExecutionContract) { c.Environment.Runtime = "host" }},
		{"network", func(c *ExecutionContract) { c.Environment.Network = "bridge" }},
		{"version", func(c *ExecutionContract) { c.Version = 2 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := contract
			tt.change(&changed)
			if requirement.Mismatch(changed, contract.TreeSHA) == "" {
				t.Fatal("accepted mismatched contract")
			}
		})
	}
	requirement.Inputs = nil
	requirements.Steps[StepTest] = requirement
	if requirements.Validate() == nil {
		t.Fatal("accepted unspecified inputs")
	}
}

func TestExecutionRequirementsVersion2(t *testing.T) {
	digest := DigestBytes([]byte("input"))
	step := StepRequirements{Command: "go test ./...", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none"}
	steps := map[StepName]StepRequirements{StepTest: step}
	for _, tt := range []struct {
		name string
		req  ExecutionRequirements
		ok   bool
	}{
		{"v1 steps", ExecutionRequirements{Version: 1, Steps: steps}, true},
		{"v1 without steps", ExecutionRequirements{Version: 1}, false},
		{"v1 cannot name a profile", ExecutionRequirements{Version: 1, Profile: &ProfileRequirement{Min: "strict"}, Steps: steps}, false},
		{"v2 profile only", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "strict"}}, true},
		{"v2 profile with version", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "standard@1"}}, true},
		{"v2 profile and steps", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "permissive"}, Steps: steps}, true},
		{"v2 steps only", ExecutionRequirements{Version: 2, Steps: steps}, true},
		{"v2 empty", ExecutionRequirements{Version: 2}, false},
		{"v2 unknown profile", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "paranoid"}}, false},
		{"v2 unknown profile version", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "strict@4"}}, false},
		{"v2 empty profile", ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{}}, false},
		{"v3", ExecutionRequirements{Version: 3, Profile: &ProfileRequirement{Min: "strict"}}, true},
		{"unknown version", ExecutionRequirements{Version: 4, Profile: &ProfileRequirement{Min: "strict"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
	bridged := step
	bridged.Network = "bridge"
	if (ExecutionRequirements{Version: 2, Steps: map[StepName]StepRequirements{StepTest: bridged}}).Validate() == nil {
		t.Fatal("version 2 steps must still require network none")
	}
	if p, ok := (ExecutionRequirements{Version: 2, Profile: &ProfileRequirement{Min: "strict"}}).MinProfile(); !ok || p.String() != "strict@1" {
		t.Fatalf("MinProfile = %v, %v", p, ok)
	}
	if _, ok := (ExecutionRequirements{Version: 1, Steps: steps}).MinProfile(); ok {
		t.Fatal("a version 1 file demands no profile")
	}
}

func TestObservedLimitsRequireVersion3(t *testing.T) {
	digest := DigestBytes([]byte("input"))
	step := StepRequirements{Command: "true", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/arm64", Network: "none", ObservedLimits: &ExecutionLimits{Memory: "512MiB"}}
	for _, version := range []int{1, 2, 3} {
		req := ExecutionRequirements{Version: version, Steps: map[StepName]StepRequirements{StepTest: step}}
		if (req.Validate() == nil) != (version == 3) {
			t.Fatalf("version %d did not enforce format boundary", version)
		}
	}
	step.ObservedLimits.Memory = "invalid"
	if (ExecutionRequirements{Version: 3, Steps: map[StepName]StepRequirements{StepTest: step}}).Validate() == nil {
		t.Fatal("invalid limits accepted")
	}
}
