package steps

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// ResolveContainer discovers a reachable CLI with the requested image already
// present. It never starts a runtime service or falls back to host execution
// once the repository has requested a container. Apple may fetch VM bootstrap
// assets during creation; the Docker-compatible backends enforce pull=never.
func ResolveContainer(cfg domain.ExecutionConfig) (*application.ContainerExecution, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.ContainerEnabled() {
		return nil, nil
	}
	runtimes := []string{"docker", "podman", "nerdctl"}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		runtimes = append([]string{"apple"}, runtimes...)
	}
	if cfg.Runtime != "" && cfg.Runtime != "auto" {
		runtimes = []string{cfg.Runtime}
	}
	var problems []string
	for _, runtimeName := range runtimes {
		executable := runtimeName
		if runtimeName == "apple" {
			executable = "container"
		}
		bin, err := exec.LookPath(executable)
		if err != nil {
			problems = append(problems, runtimeName+": not installed")
			continue
		}
		if runtimeName == "apple" {
			selected, err := resolveAppleContainer(bin, cfg)
			if err == nil {
				return selected, nil
			}
			problems = append(problems, "apple: "+err.Error())
			continue
		}
		if _, err := containerProbe(bin, "info"); err != nil {
			problems = append(problems, runtimeName+": engine unavailable")
			continue
		}
		out, err := containerProbe(bin, "image", "inspect", cfg.Image)
		if err != nil {
			problems = append(problems, runtimeName+": image not available locally")
			continue
		}
		var images []struct {
			ID           string `json:"Id"`
			OS           string `json:"Os"`
			Architecture string `json:"Architecture"`
		}
		if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 || !domain.ValidContainerImageID(images[0].ID) {
			problems = append(problems, runtimeName+": invalid image inspection")
			continue
		}
		image := images[0]
		platform := image.OS + "/" + image.Architecture
		if image.OS != "linux" || (image.Architecture != "amd64" && image.Architecture != "arm64") || (cfg.Platform != "" && cfg.Platform != platform) {
			problems = append(problems, runtimeName+": image platform does not match supported/requested Linux platform")
			continue
		}
		network := cfg.Network
		if network == "" {
			network = "none"
		}
		return &application.ContainerExecution{Binary: bin, Environment: domain.ExecutionEnvironment{Runtime: runtimeName, Image: cfg.Image, ImageID: image.ID, Platform: platform, Network: network, HostOS: runtime.GOOS, GuestOS: image.OS, ImageIDKind: "config"}}, nil
	}
	return nil, fmt.Errorf("%w (%s); start an engine and pull/build execution.image, or explicitly select execution.mode: host", application.ErrContainerUnavailable, strings.Join(problems, "; "))
}

func containerProbe(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = scrubbedEnv()
	proc.Isolate(cmd)
	return cmd.Output()
}

// runContainer mounts only the disposable tree. A nonce proves the engine sees
// this exact directory, including with remote engine contexts. Host secrets,
// dependency caches, git metadata directories and engine sockets are not mounted.
func runContainer(ctx context.Context, sc application.StepContext, dir, command string) ([]byte, error) {
	selected := sc.Container
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(token)
	name := "warden-" + nonce
	marker := ".warden-container-" + nonce
	if err := os.WriteFile(filepath.Join(dir, marker), []byte(nonce), 0o600); err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(filepath.Join(dir, marker)) }()
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var mount strings.Builder
	writer := csv.NewWriter(&mount)
	if err := writer.Write([]string{"type=bind", "src=" + abs, "dst=/workspace"}); err != nil {
		return nil, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	env := selected.Environment
	checked := "test \"$(cat /workspace/" + marker + ")\" = " + shellQuote(nonce) + " || { echo 'warden: worktree mount verification failed' >&2; exit 125; };\n" + command
	if env.Runtime == "apple" {
		return runAppleContainer(ctx, sc, abs, name, checked)
	}
	args := []string{"run", "--rm", "--name", name, "--pull=never", "--network=" + env.Network, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--platform=" + env.Platform, "--mount", strings.TrimSuffix(mount.String(), "\n"), "--workdir=/workspace", "--entrypoint=/bin/sh"}
	if uid, gid := os.Getuid(), os.Getgid(); uid >= 0 && gid >= 0 {
		if env.Runtime == "podman" {
			args = append(args, "--userns=keep-id")
		}
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(gid))
	}
	// Only Warden's explicit step inputs cross the boundary. In particular, do
	// not inherit PATH, credentials, or host absolute build-cache locations.
	for _, value := range stepInputs(sc) {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "WARDEN_HOOK", "WARDEN_BRANCH", "WARDEN_CHANGED_FILES", "WARDEN_FILES_TOUCHED", "WARDEN_LINES_CHANGED":
			args = append(args, "--env", value)
		}
	}
	// Run the inspected immutable ID, so a moved tag cannot change the image
	// between inspection and execution.
	args = append(args, "--env", "HOME=/tmp", "--env", "GOLANGCI_LINT_CACHE=/tmp/warden-lint", env.ImageID, "-c", checked)
	cmd := exec.CommandContext(ctx, selected.Binary, args...)
	cmd.Env = scrubbedEnv()
	proc.Isolate(cmd)
	out, runErr := runCaptured(cmd, sc)
	if ctx.Err() != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanup := exec.CommandContext(cleanupCtx, selected.Binary, "rm", "-f", name)
		cleanup.Env = scrubbedEnv()
		proc.Isolate(cleanup)
		if cleanupOut, err := cleanup.CombinedOutput(); err != nil {
			return out, fmt.Errorf("%w; remove container %s: %v (%s)", ctx.Err(), name, err, strings.TrimSpace(string(cleanupOut)))
		}
	}
	return out, runErr
}
