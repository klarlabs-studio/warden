package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// Apple uses an OCI index and platform manifests, not Docker's config image ID.
// Schema verified against apple/container's ImageResource at 31e3a98e471e.
type appleImage struct {
	Configuration struct {
		Name       string `json:"name"`
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"descriptor"`
	} `json:"configuration"`
	Variants []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"variants"`
}

func resolveAppleContainer(bin string, cfg domain.ExecutionConfig) (*application.ContainerExecution, error) {
	if _, err := containerProbe(bin, "system", "status"); err != nil {
		return nil, fmt.Errorf("system unavailable")
	}
	data, err := containerProbe(bin, "image", "inspect", cfg.Image)
	if err != nil {
		return nil, fmt.Errorf("image not available locally")
	}
	var images []appleImage
	if err := json.Unmarshal(data, &images); err != nil || len(images) != 1 {
		return nil, fmt.Errorf("invalid image inspection")
	}
	image := images[0]
	if !domain.ValidContainerImageID(image.Configuration.Descriptor.Digest) || image.Configuration.Name == "" || (domain.ExecutionConfig{Image: image.Configuration.Name}).Validate() != nil {
		return nil, fmt.Errorf("invalid image index or reference")
	}
	platform := cfg.Platform
	if platform == "" {
		platform = "linux/arm64"
	}
	// Rosetta translation is not enabled implicitly. It would need its own
	// explicit policy and observed execution metadata.
	if platform != "linux/arm64" {
		return nil, fmt.Errorf("apple backend currently requires linux/arm64")
	}
	var digest string
	for _, variant := range image.Variants {
		if variant.Platform.OS+"/"+variant.Platform.Architecture != platform {
			continue
		}
		if digest != "" || !domain.ValidContainerImageID(variant.Digest) {
			return nil, fmt.Errorf("ambiguous or invalid image platform")
		}
		digest = variant.Digest
	}
	if digest == "" {
		return nil, fmt.Errorf("requested platform is not available locally")
	}
	network := cfg.Network
	if network == "" {
		network = "none"
	}
	return &application.ContainerExecution{Binary: bin, Reference: image.Configuration.Name, Environment: domain.ExecutionEnvironment{Runtime: "apple", Image: cfg.Image, ImageID: digest, ImageIDKind: "manifest", ImageIndexDigest: image.Configuration.Descriptor.Digest, Platform: platform, Network: network, HostOS: runtime.GOOS, GuestOS: "linux"}}, nil
}

// Apple has no pull=never or no-new-privileges CLI option. Create stopped,
// verify the actual image index/platform, then start attached. A moved tag can
// never authorize executing a different image. The CLI may fetch VM bootstrap
// assets; documentation distinguishes that from Docker's no-pull contract.
func runAppleContainer(ctx context.Context, sc application.StepContext, dir, name, command string) (out []byte, runErr error) {
	if strings.ContainsAny(dir, ",=\r\n") {
		return nil, fmt.Errorf("apple mount parser cannot represent this worktree path")
	}
	selected := sc.Container
	env := selected.Environment
	if selected.Reference == "" || !domain.ValidContainerImageID(env.ImageIndexDigest) {
		return nil, fmt.Errorf("missing inspected Apple image identity")
	}
	args := []string{"create", "--name", name, "--cap-drop", "ALL", "--platform", env.Platform, "--mount", "type=bind,source=" + dir + ",target=/workspace", "--workdir", "/workspace", "--entrypoint", "/bin/sh"}
	if env.Network == "none" {
		args = append(args, "--network", "none", "--no-dns")
	} else {
		args = append(args, "--network", "default")
	}
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 && gid >= 0 {
		args = append(args, "--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid))
	}
	for _, value := range stepInputs(sc) {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "WARDEN_HOOK", "WARDEN_BRANCH", "WARDEN_CHANGED_FILES", "WARDEN_FILES_TOUCHED", "WARDEN_LINES_CHANGED":
			args = append(args, "--env", value)
		}
	}
	args = append(args, "--env", "HOME=/tmp", "--env", "GOLANGCI_LINT_CACHE=/tmp/warden-lint", selected.Reference, "-c", command)
	out, runErr = appleCommand(ctx, sc, args...)
	if runErr != nil {
		// Creation can fail after allocating state. Best effort cleanup; the check
		// is already a failure and cannot yield a passing attestation.
		_ = removeAppleContainer(sc, name)
		return out, runErr
	}
	defer func() {
		if err := removeAppleContainer(sc, name); err != nil {
			out = append(out, []byte("\nwarden: container cleanup failed: "+err.Error())...)
			if runErr == nil {
				runErr = err
			}
		}
	}()
	data, err := appleCommand(ctx, sc, "inspect", name)
	if err != nil {
		return append(out, data...), err
	}
	if err := verifyAppleCreated(data, name, env); err != nil {
		return append(out, []byte("\nwarden: "+err.Error())...), err
	}
	started, err := appleCommand(ctx, sc, "start", "--attach", name)
	return append(out, started...), err
}

func appleCommand(ctx context.Context, sc application.StepContext, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, sc.Container.Binary, args...)
	// Explicit --env inputs alone cross into the guest. Do not enable SSH
	// forwarding, and avoid even supplying the client with an agent socket.
	for _, value := range scrubbedEnv() {
		if !strings.HasPrefix(value, "SSH_AUTH_SOCK=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	proc.Isolate(cmd)
	return runCaptured(cmd, sc)
}

func removeAppleContainer(sc application.StepContext, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cleanupSC := sc
	cleanupSC.OnOutput = nil
	out, err := appleCommand(ctx, cleanupSC, "delete", "--force", name)
	if err != nil {
		return fmt.Errorf("remove %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func verifyAppleCreated(data []byte, name string, env domain.ExecutionEnvironment) error {
	var containers []struct {
		Configuration struct {
			ID    string `json:"id"`
			Image struct {
				Descriptor struct {
					Digest string `json:"digest"`
				} `json:"descriptor"`
			} `json:"image"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
			Networks       *[]json.RawMessage `json:"networks"`
			RuntimeHandler string             `json:"runtimeHandler"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(data, &containers); err != nil || len(containers) != 1 {
		return fmt.Errorf("invalid created-container inspection")
	}
	cfg := containers[0].Configuration
	if cfg.ID != name || cfg.Image.Descriptor.Digest != env.ImageIndexDigest || cfg.Platform.OS+"/"+cfg.Platform.Architecture != env.Platform || cfg.RuntimeHandler != "container-runtime-linux" {
		return fmt.Errorf("created container does not match inspected Linux image and platform")
	}
	if env.Network == "none" && (cfg.Networks == nil || len(*cfg.Networks) != 0) {
		return fmt.Errorf("created container unexpectedly enables networking")
	}
	return nil
}
