package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testImageJSON = `[{"Id":"` + testImageID + `","Os":"linux","Architecture":"arm64"}]`

func fakeContainerEngine(t *testing.T, dir, name, infoStatus, inspection string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake engine uses POSIX shell")
	}
	script := `#!/bin/sh
case "$1" in
info) exit ` + infoStatus + ` ;;
image) printf '%s\n' ` + shellQuote(inspection) + ` ;;
run) printf '%s\000' "$@" > "$WARDEN_TEST_CONTAINER_ARGS"
 if [ "${WARDEN_TEST_CONTAINER_WAIT:-}" = yes ]; then exec sleep 30; fi
 echo container-output
 exit "${WARDEN_TEST_CONTAINER_EXIT:-0}" ;;
rm) echo removed > "$WARDEN_TEST_CONTAINER_REMOVED" ;;
*) exit 90 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestResolveContainer(t *testing.T) {
	dir := t.TempDir()
	// Do not discover an installed engine accidentally in these tests.
	t.Setenv("PATH", dir)
	fakeContainerEngine(t, dir, "docker", "1", testImageJSON)
	fakeContainerEngine(t, dir, "podman", "0", testImageJSON)
	cfg := domain.ExecutionConfig{Image: "checks:dev"}
	selected, err := ResolveContainer(cfg)
	if err != nil || selected.Environment.Runtime != "podman" || selected.Environment.ImageID != testImageID || selected.Environment.Network != "none" {
		t.Fatalf("selection: %+v %v", selected, err)
	}
	cfg.Runtime = "docker"
	if _, err := ResolveContainer(cfg); err == nil {
		t.Fatal("explicit unavailable engine fell back")
	}
	cfg.Runtime = "podman"
	cfg.Platform = "linux/amd64"
	if _, err := ResolveContainer(cfg); err == nil {
		t.Fatal("wrong platform accepted")
	}
	cfg.Platform = "linux/arm64"
	if _, err := ResolveContainer(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "host"
	if got, err := ResolveContainer(cfg); got != nil || err != nil {
		t.Fatal("host override probed container", err)
	}
	if got, err := ResolveContainer(domain.ExecutionConfig{}); got != nil || err != nil {
		t.Fatal("default changed host execution")
	}
	for _, inspection := range []string{`{}`, `[]`, `[{"Id":"tag","Os":"linux","Architecture":"arm64"}]`, `[{"Id":"` + testImageID + `","Os":"windows","Architecture":"amd64"}]`} {
		fakeContainerEngine(t, dir, "podman", "0", inspection)
		if _, err := ResolveContainer(domain.ExecutionConfig{Runtime: "podman", Image: "checks:dev"}); err == nil {
			t.Fatal("invalid inspection accepted", inspection)
		}
	}
	fakeContainerEngine(t, dir, "podman", "1", testImageJSON)
	fakeContainerEngine(t, dir, "nerdctl", "0", testImageJSON)
	if selected, err := ResolveContainer(domain.ExecutionConfig{Image: "checks:dev"}); err != nil || selected.Environment.Runtime != "nerdctl" {
		t.Fatalf("nerdctl fallback: %+v %v", selected, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := ResolveContainer(domain.ExecutionConfig{Image: "checks:dev"}); err == nil {
		t.Fatal("missing engines fell back to host")
	} else if !errors.Is(err, application.ErrContainerUnavailable) {
		t.Fatalf("unavailable engines must be recognizable as the machine, not the change: %v", err)
	}
}

func TestShellContainerBoundary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", argsFile)
	t.Setenv("HOST_SECRET", "must-not-enter-container")
	t.Setenv("WARDEN_SECRET", "must-not-enter-container")
	selected := &application.ContainerExecution{Binary: filepath.Join(dir, "docker"), Environment: domain.ExecutionEnvironment{Runtime: "docker", Image: "checks:dev", ImageID: testImageID, Platform: "linux/arm64", Network: "none"}}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Commands: map[string]string{"test": "printf literal; exit 0"}, Container: selected}
	result, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepPass || result.Environment == nil || result.Environment.ImageID != testImageID {
		t.Fatalf("result: %+v %v", result, err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\x00")
	joined := strings.Join(args, "\n")
	for _, want := range []string{"--pull=never", "--network=none", "--cap-drop=ALL", "--entrypoint=/bin/sh", testImageID, "WARDEN_HOOK=pre_commit", "worktree mount verification failed", "printf literal; exit 0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "must-not-enter-container") || strings.Contains(joined, "docker.sock") {
		t.Fatal("host credential or engine socket exposed")
	}
	markers, err := filepath.Glob(filepath.Join(sc.WorktreeDir, ".warden-container-*"))
	if err != nil || len(markers) != 0 {
		t.Fatal("marker leaked", markers, err)
	}
	t.Setenv("WARDEN_TEST_CONTAINER_EXIT", "1")
	result, err = NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepFail {
		t.Fatal("failed container became a pass", result, err)
	}
	sc.Commands = nil
	result, err = NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil || result.Environment != nil {
		t.Fatal("skipped command claimed environment", result, err)
	}
}

func TestContainerTimeoutRemovesContainer(t *testing.T) {
	dir := t.TempDir()
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", filepath.Join(dir, "args"))
	removed := filepath.Join(dir, "removed")
	t.Setenv("WARDEN_TEST_CONTAINER_REMOVED", removed)
	t.Setenv("WARDEN_TEST_CONTAINER_WAIT", "yes")
	sc := application.StepContext{WorktreeDir: t.TempDir(), Container: &application.ContainerExecution{Binary: filepath.Join(dir, "docker"), Environment: domain.ExecutionEnvironment{Runtime: "docker", ImageID: testImageID, Platform: "linux/arm64", Network: "none"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runContainer(ctx, sc, sc.WorktreeDir, "sleep 30"); err == nil {
		t.Fatal("cancelled container passed")
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatal("container not explicitly removed after cancellation", err)
	}
}

// Opt-in integration test uses a preloaded Linux image with /bin/sh and core
// utilities. No daemon is started and no image is downloaded by the test.
func TestContainerExecutionIntegration(t *testing.T) {
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	runtimeName := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if runtimeName == "" {
		runtimeName = "docker"
	}
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: runtimeName, Image: image})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("WARDEN_CONTAINER_TEST_SECRET", "host-only")
	sc := application.StepContext{WorktreeDir: dir, Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": `test -z "$WARDEN_CONTAINER_TEST_SECRET" && test "$WARDEN_HOOK" = pre_commit && uname -s > observed-os && printf container > artifact`}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := NewShellStep(domain.StepTest, "test").Run(ctx, sc)
	if err != nil || result.Status != domain.StepPass {
		t.Fatalf("result: %+v %v", result, err)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "artifact"))
	if err != nil || string(artifact) != "container" {
		t.Fatal("missing artifact", string(artifact), err)
	}
	platform, err := os.ReadFile(filepath.Join(dir, "observed-os"))
	if err != nil || strings.TrimSpace(string(platform)) != "Linux" {
		t.Fatal("command did not observe Linux", string(platform), err)
	}
}

func TestContainerScannerVersionIsCheckedInsideImage(t *testing.T) {
	dir := t.TempDir()
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", filepath.Join(dir, "args"))
	root := t.TempDir()
	workflows := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte("env:\n  NOX_VERSION: 1.15.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: root, Commands: map[string]string{"security-scan": "nox scan . -severity-threshold high"}, Container: &application.ContainerExecution{Binary: filepath.Join(dir, "docker"), Environment: domain.ExecutionEnvironment{Runtime: "docker", ImageID: testImageID, Platform: "linux/arm64", Network: "none"}}}
	result, err := NewSecurityScanStep(domain.StepSecurityScan).Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepFail || !strings.Contains(result.Summary, "container scanner version") {
		t.Fatalf("unverifiable pinned version passed: %+v %v", result, err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "'nox' version") {
		t.Fatal("version was not probed inside the image")
	}
	off := false
	sc.SecurityScan.VersionCheck = &off
	result, err = NewSecurityScanStep(domain.StepSecurityScan).Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepPass || result.Environment == nil {
		t.Fatalf("explicit version-check opt out failed: %+v %v", result, err)
	}
}
