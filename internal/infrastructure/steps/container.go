package steps

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	var problems []string
	for _, runtimeName := range containerRuntimes(cfg) {
		bin, err := exec.LookPath(runtimeExecutable(runtimeName))
		if err != nil {
			problems = append(problems, runtimeName+": not installed")
			continue
		}
		var selected *application.ContainerExecution
		if runtimeName == "apple" {
			selected, err = resolveAppleContainer(bin, cfg)
		} else {
			selected, err = resolveCompatContainer(runtimeName, bin, cfg)
		}
		if err != nil {
			problems = append(problems, runtimeName+": "+err.Error())
			continue
		}
		return selected, nil
	}
	return nil, unavailable(problems)
}

// SampleEnvironment reports the execution environment ResolveContainer would
// bind (runtime, image ID, platform) by inspecting the configured image only.
// It never probes the engine's info or system status, never pulls and never
// creates or runs a container, so a background watcher can afford to call it
// repeatedly. It walks the same runtimes in the same order as ResolveContainer
// and returns the first whose local store holds a usable image; it is an
// observation for change detection, not a selection: an evaluation still
// resolves the runtime itself, and only the environment a step actually ran
// in is ever recorded. A host-mode configuration has no environment to sample
// and returns the zero value.
func SampleEnvironment(ctx context.Context, cfg domain.ExecutionConfig) (domain.ExecutionEnvironment, error) {
	if err := cfg.Validate(); err != nil {
		return domain.ExecutionEnvironment{}, err
	}
	if !cfg.ContainerEnabled() {
		return domain.ExecutionEnvironment{}, nil
	}
	probe := func(bin string, args ...string) ([]byte, error) {
		return containerProbeContext(ctx, bin, args...)
	}
	var problems []string
	for _, runtimeName := range containerRuntimes(cfg) {
		bin, err := exec.LookPath(runtimeExecutable(runtimeName))
		if err != nil {
			problems = append(problems, runtimeName+": not installed")
			continue
		}
		var env domain.ExecutionEnvironment
		if runtimeName == "apple" {
			var selected *application.ContainerExecution
			if selected, err = inspectAppleImage(probe, bin, cfg); err == nil {
				env = selected.Environment
			}
		} else {
			_, env, err = inspectCompatImage(probe, runtimeName, bin, cfg)
		}
		if ctx.Err() != nil {
			return domain.ExecutionEnvironment{}, ctx.Err()
		}
		if err != nil {
			problems = append(problems, runtimeName+": "+err.Error())
			continue
		}
		return env, nil
	}
	return domain.ExecutionEnvironment{}, unavailable(problems)
}

// containerRuntimes is the order runtimes are tried in: Apple's first on Apple
// silicon, then the Docker-compatible engines, or only the configured one.
func containerRuntimes(cfg domain.ExecutionConfig) []string {
	if cfg.Runtime != "" && cfg.Runtime != "auto" {
		return []string{cfg.Runtime}
	}
	runtimes := []string{"docker", "podman", "nerdctl"}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		runtimes = append([]string{"apple"}, runtimes...)
	}
	return runtimes
}

// runtimeExecutable is the CLI a runtime is driven through.
func runtimeExecutable(runtimeName string) string {
	if runtimeName == "apple" {
		return "container"
	}
	return runtimeName
}

func unavailable(problems []string) error {
	return fmt.Errorf("%w (%s); start an engine and pull/build execution.image, or explicitly select execution.mode: host", application.ErrContainerUnavailable, strings.Join(problems, "; "))
}

// containerCleanupError is returned for a container whose command exited
// successfully but which could not be removed afterwards. The command's
// verdict stands: the error is a note, never a failure of the check. Only
// splitCleanup should look inside it; any caller that does not still sees an
// error, so a missed case fails closed.
type containerCleanupError struct{ err error }

func (e *containerCleanupError) Error() string { return "container cleanup failed: " + e.err.Error() }
func (e *containerCleanupError) Unwrap() error { return e.err }

// splitCleanup separates a failed removal after a successful command from the
// command's own result: it returns a note describing the failed removal (""
// when there was none) and the command's error (nil for that case).
func splitCleanup(err error) (note string, commandErr error) {
	var cleanup *containerCleanupError
	if errors.As(err, &cleanup) {
		return cleanup.Error(), nil
	}
	return "", err
}

// probeFunc runs one read-only engine CLI query.
type probeFunc func(bin string, args ...string) ([]byte, error)

func containerProbe(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return containerProbeContext(ctx, bin, args...)
}

func containerProbeContext(ctx context.Context, bin string, args ...string) ([]byte, error) {
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
	name := containerName(nonce)
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
	if selected.Reference == "" {
		return nil, fmt.Errorf("container execution has no inspected image reference to run")
	}
	args := []string{"run", "--rm", "--name", name, "--pull=never", "--network=" + env.Network, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--platform=" + env.Platform, "--mount", strings.TrimSuffix(mount.String(), "\n"), "--workdir=/workspace", "--entrypoint=/bin/sh"}
	args = append(args, labelArgs()...)
	args = append(args, compatUserArgs(selected, os.Getuid(), os.Getgid())...)
	// Only Warden's explicit step inputs cross the boundary. In particular, do
	// not inherit PATH, credentials, or host absolute build-cache locations.
	for _, value := range stepInputs(sc) {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "WARDEN_HOOK", "WARDEN_BRANCH", "WARDEN_CHANGED_FILES", "WARDEN_FILES_TOUCHED", "WARDEN_LINES_CHANGED":
			args = append(args, "--env", value)
		}
	}
	// Run the inspected immutable reference, so a moved tag cannot change the
	// image between inspection and execution.
	args = append(args, "--env", "HOME=/tmp", "--env", "GOLANGCI_LINT_CACHE=/tmp/warden-lint", selected.Reference, "-c", checked)
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
