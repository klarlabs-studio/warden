package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// ExecutionConfig selects the environment for configured shell checks. Auto
// preserves host execution until a repository opts in by naming an image.
type ExecutionConfig struct {
	Mode     string `yaml:"mode" json:"mode,omitempty"`
	Runtime  string `yaml:"runtime" json:"runtime,omitempty"`
	Image    string `yaml:"image" json:"image,omitempty"`
	Platform string `yaml:"platform" json:"platform,omitempty"`
	Network  string `yaml:"network" json:"network,omitempty"`
}

func (c ExecutionConfig) ContainerEnabled() bool { return c.Mode != "host" && c.Image != "" }

func (c ExecutionConfig) Validate() error {
	switch c.Mode {
	case "", "auto", "host", "container":
	default:
		return fmt.Errorf("execution.mode must be auto, host, or container")
	}
	switch c.Runtime {
	case "", "auto", "docker", "podman", "nerdctl", "apple":
	default:
		return fmt.Errorf("execution.runtime must be auto, docker, podman, nerdctl, or apple")
	}
	if (c.Mode == "container" || (c.Mode != "host" && c.Runtime != "" && c.Runtime != "auto")) && c.Image == "" {
		return fmt.Errorf("execution.image is required for container execution")
	}
	if c.Image != "" && (strings.HasPrefix(c.Image, "-") || strings.ContainsAny(c.Image, " \t\r\n\x00")) {
		return fmt.Errorf("execution.image must be a single image reference")
	}
	switch c.Platform {
	case "", "linux/amd64", "linux/arm64":
	default:
		return fmt.Errorf("execution.platform must be linux/amd64 or linux/arm64")
	}
	switch c.Network {
	case "", "none", "bridge":
	default:
		return fmt.Errorf("execution.network must be none or bridge")
	}
	return nil
}

func (c ExecutionConfig) OverlayOnto(base ExecutionConfig) ExecutionConfig {
	if c.Mode != "" {
		base.Mode = c.Mode
	}
	if c.Runtime != "" {
		base.Runtime = c.Runtime
	}
	if c.Image != "" {
		base.Image = c.Image
	}
	if c.Platform != "" {
		base.Platform = c.Platform
	}
	if c.Network != "" {
		base.Network = c.Network
	}
	return base
}

// ExecutionEnvironment records an observed command environment, not a promise
// that every step (including native git operations and agents) ran there.
type ExecutionEnvironment struct {
	Runtime          string `json:"runtime"`
	Image            string `json:"image"`
	ImageID          string `json:"image_id"`
	Platform         string `json:"platform"`
	Network          string `json:"network"`
	HostOS           string `json:"host_os,omitempty"`
	GuestOS          string `json:"guest_os,omitempty"`
	ImageIDKind      string `json:"image_id_kind,omitempty"`
	ImageIndexDigest string `json:"image_index_digest,omitempty"`
}

var containerImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidContainerImageID(id string) bool { return containerImageID.MatchString(id) }

// ContainerChecks reports only recorded execution, never inferring container
// use from the absence of host metadata or from requested configuration.
func (r RunRecord) ContainerChecks(steps []StepName, imageID string) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		env, ok := r.Environments[step]
		if !ok || !ValidContainerImageID(env.ImageID) || (imageID != "" && env.ImageID != imageID) {
			return false
		}
		switch env.Runtime {
		case "apple":
			if env.ImageIDKind != "manifest" || !ValidContainerImageID(env.ImageIndexDigest) || env.GuestOS != "linux" {
				return false
			}
		case "docker", "podman", "nerdctl":
		default:
			return false
		}
		if env.GuestOS != "" && env.GuestOS != "linux" {
			return false
		}
		if env.Platform != "linux/amd64" && env.Platform != "linux/arm64" {
			return false
		}
	}
	return true
}
