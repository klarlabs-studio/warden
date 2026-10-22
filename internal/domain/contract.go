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

// ExecutionRequirementsV2 is the requirements format that adds a minimum
// compliance profile (ADR 0007). Version 1 files keep their exact meaning.
// A warden that predates version 2 rejects such a file (its version check and
// DisallowUnknownFields both refuse it), so a consumer pinned to an older
// release fails safe: verification is refused and CI runs its checks.
const ExecutionRequirementsV2 = 2

// ExecutionRequirementsV3 adds initial workload observations to consumer policy.
const ExecutionRequirementsV3 = 3

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
// being evaluated. Identity fields are exact expectations; observed_limits are upper bounds.
type ExecutionRequirements struct {
	Version int                           `json:"version"`
	Profile *ProfileRequirement           `json:"profile,omitempty"`
	Steps   map[StepName]StepRequirements `json:"steps"`
}

// ProfileRequirement is the version 2 profile clause: the note must conform to
// at least Min, recomputed from its signed observations (see
// service.Conformance), never accepted on the strength of the recorded name.
type ProfileRequirement struct {
	// Min is a profile name ("strict") or name@version ("strict@1").
	Min string `json:"min"`
}

// MinProfile returns the minimum profile a version 2 file demands, or false
// when it demands none.
func (r ExecutionRequirements) MinProfile() (ComplianceProfile, bool) {
	if r.Profile == nil {
		return ComplianceProfile{}, false
	}
	p, err := ParseComplianceProfile(r.Profile.Min)
	return p, err == nil
}

type StepRequirements struct {
	// ObservedLimits requires a fresh initial probe, bounded resources and no
	// IPv4 or non-loopback IPv6 routes. An empty object checks routes only.
	ObservedLimits      *ExecutionLimits  `json:"observed_limits,omitempty"`
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
	switch r.Version {
	case ExecutionContractVersion:
		if len(r.Steps) == 0 {
			return fmt.Errorf("requirements must use version 1 and name at least one step")
		}
		if r.Profile != nil {
			return fmt.Errorf("requirements version 1 cannot name a profile; use version 2")
		}
	case ExecutionRequirementsV2, ExecutionRequirementsV3:
		if r.Profile == nil && len(r.Steps) == 0 {
			return fmt.Errorf("requirements version %d must name a profile, at least one step, or both", r.Version)
		}
		if r.Profile != nil {
			if _, err := ParseComplianceProfile(r.Profile.Min); err != nil {
				return fmt.Errorf("requirements profile.min: %w", err)
			}
		}
	default:
		return fmt.Errorf("requirements must use version 1, 2 or 3")
	}
	for name := range r.Steps {
		step := r.Steps[name]
		if step.ObservedLimits != nil {
			if r.Version != ExecutionRequirementsV3 {
				return fmt.Errorf("observed_limits requires requirements version 3")
			}
			if err := step.ObservedLimits.Validate(); err != nil {
				return fmt.Errorf("requirements for %s: %w", name, err)
			}
		}
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
			return fmt.Errorf("requirements version %d requires network none", r.Version)
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
