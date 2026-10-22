package domain

import (
	"fmt"
	"regexp"
	"strconv"
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
	// Limits bounds what a container shell check may consume. Nil means no
	// limits were asked for. They are a request to the engine; what reached
	// the workload is a separate observation (ADR 0008).
	Limits *ExecutionLimits `yaml:"limits" json:"limits,omitempty"`
	// AttestObservations opts into the signed initial-probe field after readers
	// have upgraded. Nil inherits the parent setting; the default is off.
	AttestObservations *bool `yaml:"attest_observations" json:"attest_observations,omitempty"`
}

// ExecutionLimits is `execution.limits`. Each field is optional; an empty one
// leaves that resource unbounded.
type ExecutionLimits struct {
	// Memory is a binary size in whole MiB: "512MiB", "4GiB". It also caps
	// swap, so a check cannot page past it.
	Memory string `yaml:"memory" json:"memory,omitempty"`
	// CPUs is a decimal count of CPUs, at most three decimal places: "2", "1.5".
	CPUs string `yaml:"cpus" json:"cpus,omitempty"`
	// Pids caps the processes and threads in the container.
	Pids int `yaml:"pids" json:"pids,omitempty"`
}

// minLimitMemoryMiB is the smallest memory limit accepted: engines refuse
// less (Docker's floor is 6 MB), and no check runs in less.
const minLimitMemoryMiB = 6

// MemoryMiB returns the memory limit in MiB, or 0 when none is set.
func (l ExecutionLimits) MemoryMiB() (int64, error) {
	s := strings.TrimSpace(l.Memory)
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mib    int64
	}{{"TiB", 1 << 20}, {"GiB", 1 << 10}, {"MiB", 1}}
	for _, u := range units {
		num, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
		if err != nil || n <= 0 || n > (1<<40)/u.mib {
			break
		}
		if mib := n * u.mib; mib >= minLimitMemoryMiB {
			return mib, nil
		}
		return 0, fmt.Errorf("execution.limits.memory %q is below the %d MiB minimum", l.Memory, minLimitMemoryMiB)
	}
	return 0, fmt.Errorf("execution.limits.memory %q must be a whole number of MiB, GiB or TiB, e.g. 512MiB or 4GiB", l.Memory)
}

// MilliCPUs returns the CPU limit in thousandths of a CPU, or 0 when none is
// set.
func (l ExecutionLimits) MilliCPUs() (int64, error) {
	s := strings.TrimSpace(l.CPUs)
	if s == "" {
		return 0, nil
	}
	whole, frac, dotted := strings.Cut(s, ".")
	invalid := fmt.Errorf("execution.limits.cpus %q must be a positive decimal with at most three places, e.g. 2 or 1.5", l.CPUs)
	if whole == "" || (dotted && frac == "") || len(frac) > 3 || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		return 0, invalid
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > 1<<20 {
		return 0, invalid
	}
	var f int64
	if frac != "" {
		if f, err = strconv.ParseInt(frac+strings.Repeat("0", 3-len(frac)), 10, 64); err != nil || f < 0 {
			return 0, invalid
		}
	}
	if m := w*1000 + f; m > 0 {
		return m, nil
	}
	return 0, invalid
}

// Validate checks each set field.
func (l ExecutionLimits) Validate() error {
	if _, err := l.MemoryMiB(); err != nil {
		return err
	}
	if _, err := l.MilliCPUs(); err != nil {
		return err
	}
	if l.Pids < 0 {
		return fmt.Errorf("execution.limits.pids must be a positive number")
	}
	return nil
}

// overlayOnto sets each field l sets, keeping the rest of base.
func (l ExecutionLimits) overlayOnto(base ExecutionLimits) ExecutionLimits {
	if l.Memory != "" {
		base.Memory = l.Memory
	}
	if l.CPUs != "" {
		base.CPUs = l.CPUs
	}
	if l.Pids != 0 {
		base.Pids = l.Pids
	}
	return base
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
	if c.Limits != nil {
		return c.Limits.Validate()
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
	if c.AttestObservations != nil {
		value := *c.AttestObservations
		base.AttestObservations = &value
	}
	if c.Limits != nil {
		var from ExecutionLimits
		if base.Limits != nil {
			from = *base.Limits
		}
		merged := c.Limits.overlayOnto(from)
		base.Limits = &merged
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
