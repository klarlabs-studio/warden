package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

const ExecutionContractVersion = 1

// ExecutionContract contains observations from one fresh command execution.
// Inputs are tracked manifest contents, not installed-package attestations.
// Digests avoid publishing command text and contextual environment values.
type ExecutionContract struct {
	Version             int                  `json:"version"`
	CommandDigest       string               `json:"command_digest"`
	ConfigurationDigest string               `json:"configuration_digest"`
	ContextDigest       string               `json:"context_digest"`
	TreeSHA             string               `json:"tree_sha"`
	Inputs              map[string]string    `json:"inputs"`
	Environment         ExecutionEnvironment `json:"environment"`
}

// ExecutionRequirements comes from protected consumer policy, never the note
// being evaluated. Every field in a required step is an exact expectation.
type ExecutionRequirements struct {
	Version int                           `json:"version"`
	Steps   map[StepName]StepRequirements `json:"steps"`
}

type StepRequirements struct {
	Command             string            `json:"command"`
	ConfigurationDigest string            `json:"configuration_digest"`
	ContextDigest       string            `json:"context_digest"`
	Inputs              map[string]string `json:"inputs"`
	ImageID             string            `json:"image_id"`
	ImageIDKind         string            `json:"image_id_kind"`
	Platform            string            `json:"platform"`
	Network             string            `json:"network"`
}

func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestJSON uses encoding/json's deterministic struct and map ordering.
func DigestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return DigestBytes(data), nil
}

var gitTreeID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func ValidTreeSHA(sha string) bool { return gitTreeID.MatchString(sha) }

func (r ExecutionRequirements) Validate() error {
	if r.Version != ExecutionContractVersion || len(r.Steps) == 0 {
		return fmt.Errorf("requirements must use version 1 and name at least one step")
	}
	for name, step := range r.Steps {
		if !name.Valid() || strings.TrimSpace(step.Command) == "" {
			return fmt.Errorf("requirements contain an invalid step or empty command")
		}
		if !ValidContainerImageID(step.ConfigurationDigest) || !ValidContainerImageID(step.ContextDigest) || !ValidContainerImageID(step.ImageID) {
			return fmt.Errorf("requirements for %s need sha256 configuration, context, and image digests", name)
		}
		if step.ImageIDKind != "config" && step.ImageIDKind != "manifest" {
			return fmt.Errorf("requirements for %s need image_id_kind config or manifest", name)
		}
		if step.Platform != "linux/amd64" && step.Platform != "linux/arm64" {
			return fmt.Errorf("requirements for %s need a supported Linux platform", name)
		}
		if step.Network != "none" {
			return fmt.Errorf("requirements version 1 requires network none")
		}
		if step.Inputs == nil {
			return fmt.Errorf("requirements for %s must explicitly provide inputs ({} when empty)", name)
		}
		for file, digest := range step.Inputs {
			if file == "." || strings.ContainsAny(file, "\\\x00") || path.IsAbs(file) || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") || !ValidContainerImageID(digest) {
				return fmt.Errorf("requirements for %s contain an invalid input path or digest", name)
			}
		}
	}
	return nil
}

// Mismatch compares every required observation. The caller separately verifies
// the note's trust and supplies this commit's tree from Git, not from the note.
func (r StepRequirements) Mismatch(c ExecutionContract, tree string) string {
	switch {
	case c.Version != ExecutionContractVersion:
		return "missing or unsupported execution contract"
	case !ValidTreeSHA(tree) || c.TreeSHA != tree:
		return "checked tree does not match the gated commit"
	case c.CommandDigest != DigestBytes([]byte(r.Command)):
		return "command differs"
	case c.ConfigurationDigest != r.ConfigurationDigest:
		return "configuration differs"
	case c.ContextDigest != r.ContextDigest:
		return "step context differs"
	case c.Inputs == nil || !maps.Equal(c.Inputs, r.Inputs):
		return "dependency-file inputs differ or are incomplete"
	case c.Environment.ImageID != r.ImageID || c.Environment.ImageIDKind != r.ImageIDKind:
		return "image identity differs"
	case c.Environment.Platform != r.Platform || c.Environment.Network != r.Network:
		return "execution platform or network differs"
	}
	// Reuse the container observation validation, including Apple's manifest/index
	// requirements. Host metadata never becomes container evidence by omission.
	rec := RunRecord{Environments: map[StepName]ExecutionEnvironment{StepTest: c.Environment}}
	if !rec.ContainerChecks([]StepName{StepTest}, r.ImageID) {
		return "container identity is incomplete"
	}
	return ""
}

func (r ExecutionRequirements) OrderedSteps() []StepName {
	names := make([]StepName, 0, len(r.Steps))
	for name := range r.Steps {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
