package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// loggingEngine is a fake engine CLI that records every invocation and
// answers only `image inspect`; any other command fails loudly.
func loggingEngine(t *testing.T, dir, name, inspection string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake engine uses POSIX shell")
	}
	calls := filepath.Join(dir, name+".calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + shellQuote(calls) + `
case "$1 $2" in
"image inspect") printf '%s\n' ` + shellQuote(inspection) + ` ;;
*) exit 90 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return calls
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// The watcher samples the environment on a timer, so the sample must be the
// cheap, side-effect-free part of resolution: `image inspect` and nothing
// else — no engine info, no system status, no pull, no container.
func TestSampleEnvironmentOnlyInspects(t *testing.T) {
	appleJSON := appleImageJSON
	for _, tc := range []struct {
		runtime, executable, inspection string
		want                            domain.ExecutionEnvironment
	}{
		{"docker", "docker", testImageJSON, domain.ExecutionEnvironment{Runtime: "docker", Image: "checks:dev", ImageID: testImageID, Platform: "linux/arm64", Network: "none", ImageIDKind: "config"}},
		{"podman", "podman", `[{"Id":"` + testBareImageID + `","Os":"linux","Architecture":"arm64"}]`, domain.ExecutionEnvironment{Runtime: "podman", Image: "checks:dev", ImageID: testImageID, Platform: "linux/arm64", Network: "none", ImageIDKind: "config"}},
		{"nerdctl", "nerdctl", testImageJSON, domain.ExecutionEnvironment{Runtime: "nerdctl", Image: "checks:dev", ImageID: testImageID, Platform: "linux/arm64", Network: "none", ImageIDKind: "config"}},
		{"apple", "container", appleJSON, domain.ExecutionEnvironment{Runtime: "apple", Image: "checks:dev", ImageID: testImageID, Platform: "linux/arm64", Network: "none", ImageIDKind: "manifest", ImageIndexDigest: appleIndex}},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			calls := loggingEngine(t, dir, tc.executable, tc.inspection)
			env, err := SampleEnvironment(context.Background(), domain.ExecutionConfig{Runtime: tc.runtime, Image: "checks:dev"})
			if err != nil {
				t.Fatal(err)
			}
			tc.want.HostOS, tc.want.GuestOS = runtime.GOOS, "linux"
			if env != tc.want {
				t.Fatalf("sampled %+v\nwant    %+v", env, tc.want)
			}
			if got := readCalls(t, calls); len(got) != 1 || got[0] != "image inspect checks:dev" {
				t.Fatalf("engine invocations %q, want only the image inspection", got)
			}
		})
	}
}

// The sample must agree with what an evaluation resolves, or a change it
// reports would not be the change a commit sees.
func TestSampleEnvironmentMatchesResolution(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	cfg := domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev", Network: "none"}
	selected, err := ResolveContainer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env, err := SampleEnvironment(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if env != selected.Environment {
		t.Fatalf("sample %+v differs from resolution %+v", env, selected.Environment)
	}
}

func TestSampleEnvironmentUnavailableAndHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := SampleEnvironment(context.Background(), domain.ExecutionConfig{Image: "checks:dev"}); !errors.Is(err, application.ErrContainerUnavailable) {
		t.Fatalf("no engine installed: %v", err)
	}
	loggingEngine(t, dir, "docker", `[]`)
	if _, err := SampleEnvironment(context.Background(), domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev"}); !errors.Is(err, application.ErrContainerUnavailable) {
		t.Fatalf("invalid inspection: %v", err)
	}
	if env, err := SampleEnvironment(context.Background(), domain.ExecutionConfig{}); err != nil || env != (domain.ExecutionEnvironment{}) {
		t.Fatalf("host mode: %+v %v", env, err)
	}
	if _, err := SampleEnvironment(context.Background(), domain.ExecutionConfig{Runtime: "bogus", Image: "checks:dev"}); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loggingEngine(t, dir, "docker", testImageJSON)
	if _, err := SampleEnvironment(ctx, domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped sample must be inconclusive, got %v", err)
	}
}
