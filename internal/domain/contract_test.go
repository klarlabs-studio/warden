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
