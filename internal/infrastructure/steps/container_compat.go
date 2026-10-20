package steps

import (
	"encoding/json"
	"errors"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// compatImage is the subset of `image inspect` output shared by Docker,
// Podman and nerdctl's Docker-compatible mode.
type compatImage struct {
	ID           string   `json:"Id"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	RepoDigests  []string `json:"RepoDigests"`
}

var bareImageID = regexp.MustCompile(`^[a-f0-9]{64}$`)

var errRootlessUnknown = errors.New("cannot determine whether the engine is rootless")

// resolveCompatContainer selects a Docker-compatible engine whose store holds
// the requested image, and binds the immutable reference it will run to the
// inspected config ID.
func resolveCompatContainer(runtimeName, bin string, cfg domain.ExecutionConfig) (*application.ContainerExecution, error) {
	rootless, err := probeRootless(runtimeName, bin)
	if err != nil {
		return nil, err
	}
	image, env, err := inspectCompatImage(containerProbe, runtimeName, bin, cfg)
	if err != nil {
		return nil, err
	}
	reference, err := compatRunReference(runtimeName, bin, image, env.Platform)
	if err != nil {
		return nil, err
	}
	return &application.ContainerExecution{Binary: bin, Reference: reference, Rootless: rootless, Environment: env}, nil
}

// inspectCompatImage reads the configured image's config ID and platform from
// `image inspect` alone and returns the environment it would run in.
func inspectCompatImage(probe probeFunc, runtimeName, bin string, cfg domain.ExecutionConfig) (compatImage, domain.ExecutionEnvironment, error) {
	out, err := probe(bin, "image", "inspect", cfg.Image)
	if err != nil {
		return compatImage{}, domain.ExecutionEnvironment{}, errors.New("image not available locally")
	}
	image, err := parseCompatInspection(runtimeName, out)
	if err != nil {
		return compatImage{}, domain.ExecutionEnvironment{}, err
	}
	platform := image.OS + "/" + image.Architecture
	if image.OS != "linux" || (image.Architecture != "amd64" && image.Architecture != "arm64") || (cfg.Platform != "" && cfg.Platform != platform) {
		return compatImage{}, domain.ExecutionEnvironment{}, errors.New("image platform does not match supported/requested Linux platform")
	}
	network := cfg.Network
	if network == "" {
		network = "none"
	}
	return image, domain.ExecutionEnvironment{Runtime: runtimeName, Image: cfg.Image, ImageID: image.ID, Platform: platform, Network: network, HostOS: runtime.GOOS, GuestOS: image.OS, ImageIDKind: "config"}, nil
}

// probeRootless doubles as the reachability probe. A rootless engine maps
// container root to the invoking user, so the UID passed to `run` depends on
// it; an answer warden cannot read fails selection rather than guessing.
func probeRootless(runtimeName, bin string) (bool, error) {
	format := "{{json .SecurityOptions}}"
	if runtimeName == "podman" {
		format = "{{.Host.Security.Rootless}}"
	}
	out, err := containerProbe(bin, "info", "--format", format)
	if err != nil {
		return false, errors.New("engine unavailable")
	}
	return parseRootless(runtimeName, out)
}

func parseRootless(runtimeName string, out []byte) (bool, error) {
	answer := strings.TrimSpace(string(out))
	if runtimeName == "podman" {
		rootless, err := strconv.ParseBool(answer)
		if err != nil {
			return false, errRootlessUnknown
		}
		return rootless, nil
	}
	var options []string
	if err := json.Unmarshal([]byte(answer), &options); err != nil {
		return false, errRootlessUnknown
	}
	return slices.Contains(options, "name=rootless"), nil
}

// parseCompatInspection normalizes the config ID. Podman prints it as bare
// hex; Docker and nerdctl prefix the algorithm.
func parseCompatInspection(runtimeName string, out []byte) (compatImage, error) {
	invalid := errors.New("invalid image inspection")
	var images []compatImage
	if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 {
		return compatImage{}, invalid
	}
	image := images[0]
	if runtimeName == "podman" && bareImageID.MatchString(image.ID) {
		image.ID = "sha256:" + image.ID
	}
	if !domain.ValidContainerImageID(image.ID) {
		return compatImage{}, invalid
	}
	return image, nil
}

// compatRunReference returns what `run` is given. Docker runs the config ID
// directly; Podman takes it as bare hex. nerdctl resolves `run` references
// only by name or containerd target digest, so the target digest is run and
// re-inspected for the selected platform: content addressing then fixes the
// config it resolves to, and a different config fails selection.
func compatRunReference(runtimeName, bin string, image compatImage, platform string) (string, error) {
	switch runtimeName {
	case "podman":
		return strings.TrimPrefix(image.ID, "sha256:"), nil
	case "nerdctl":
		target, err := nerdctlTargetDigest(image.RepoDigests)
		if err != nil {
			return "", err
		}
		out, err := containerProbe(bin, "image", "inspect", "--platform="+platform, target)
		if err != nil {
			return "", errors.New("image target digest not available locally")
		}
		resolved, err := parseCompatInspection(runtimeName, out)
		if err != nil || resolved.ID != image.ID {
			return "", errors.New("image target digest does not resolve to the inspected image")
		}
		return target, nil
	default:
		return image.ID, nil
	}
}

func nerdctlTargetDigest(repoDigests []string) (string, error) {
	target := ""
	for _, repoDigest := range repoDigests {
		_, digest, ok := strings.Cut(repoDigest, "@")
		if !ok || !domain.ValidContainerImageID(digest) || (target != "" && digest != target) {
			return "", errors.New("invalid image target digest")
		}
		target = digest
	}
	if target == "" {
		return "", errors.New("invalid image target digest")
	}
	return target, nil
}

// compatUserArgs runs the check as the invoking user on the host. Rootful
// engines take that UID/GID directly. Rootless Podman maps it with keep-id.
// Rootless Docker and nerdctl map only container root to the invoking user;
// any other UID lands on a subordinate ID that cannot write the worktree, so
// the check runs as container root, which is the invoking user on the host,
// with capabilities still dropped.
func compatUserArgs(selected *application.ContainerExecution, uid, gid int) []string {
	if uid < 0 || gid < 0 {
		return nil
	}
	user := strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
	switch {
	case !selected.Rootless:
		return []string{"--user", user}
	case selected.Environment.Runtime == "podman":
		return []string{"--userns=keep-id", "--user", user}
	default:
		return []string{"--user", "0:0"}
	}
}
