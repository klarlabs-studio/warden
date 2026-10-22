package steps

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

const testBareImageID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// Each engine names the same inspected config differently, and the reference
// handed to `run` must be one that engine resolves to that config.
func TestResolveContainerRunReferences(t *testing.T) {
	podmanJSON := `[{"Id":"` + testBareImageID + `","Os":"linux","Architecture":"arm64"}]`
	for _, tc := range []struct {
		runtime, inspection, reference string
	}{
		{"docker", testImageJSON, testImageID},
		{"podman", podmanJSON, testBareImageID},
		{"podman", testImageJSON, testBareImageID},
		{"nerdctl", testImageJSON, testTargetDigest},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			fakeContainerEngine(t, dir, tc.runtime, "0", tc.inspection)
			selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: tc.runtime, Image: "checks:dev"})
			if err != nil {
				t.Fatal(err)
			}
			if selected.Environment.ImageID != testImageID || selected.Environment.ImageIDKind != "config" || selected.Reference != tc.reference {
				t.Fatalf("identity: %+v", selected)
			}
		})
	}
}

func TestResolveNerdctlRejectsUnboundTarget(t *testing.T) {
	other := `[{"Id":"sha256:` + strings.Repeat("d", 64) + `","Os":"linux","Architecture":"arm64","RepoDigests":["docker.io/library/checks@` + testTargetDigest + `"]}]`
	for name, tc := range map[string]struct{ inspection, target string }{
		"target resolves elsewhere": {testImageJSON, other},
		"target inspection invalid": {testImageJSON, `[]`},
		"no repo digest":            {`[{"Id":"` + testImageID + `","Os":"linux","Architecture":"arm64"}]`, ""},
		"malformed repo digest":     {`[{"Id":"` + testImageID + `","Os":"linux","Architecture":"arm64","RepoDigests":["checks@sha256:short"]}]`, ""},
		"conflicting repo digests":  {`[{"Id":"` + testImageID + `","Os":"linux","Architecture":"arm64","RepoDigests":["a@` + testTargetDigest + `","b@sha256:` + strings.Repeat("e", 64) + `"]}]`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("WARDEN_TEST_TARGET_INSPECTION", tc.target)
			fakeContainerEngine(t, dir, "nerdctl", "0", tc.inspection)
			if selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "nerdctl", Image: "checks:dev"}); err == nil {
				t.Fatalf("unbound nerdctl reference accepted: %+v", selected)
			}
		})
	}
}

func TestResolveContainerRootlessMode(t *testing.T) {
	for _, tc := range []struct {
		runtime, answer string
		rootless, ok    bool
	}{
		{"docker", "false", false, true},
		{"docker", "true", true, true},
		{"nerdctl", "true", true, true},
		{"podman", "true", true, true},
		{"podman", "false", false, true},
		{"podman", "garbage", false, false},
		{"docker", "garbage", false, false},
	} {
		t.Run(tc.runtime+"/"+tc.answer, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("WARDEN_TEST_CONTAINER_ROOTLESS", tc.answer)
			fakeContainerEngine(t, dir, tc.runtime, "0", testImageJSON)
			selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: tc.runtime, Image: "checks:dev"})
			if !tc.ok {
				if err == nil || !strings.Contains(err.Error(), "rootless") {
					t.Fatalf("unreadable rootless mode accepted: %+v %v", selected, err)
				}
				return
			}
			if err != nil || selected.Rootless != tc.rootless {
				t.Fatalf("rootless: %+v %v", selected, err)
			}
		})
	}
}

func TestCompatUserArgs(t *testing.T) {
	for _, tc := range []struct {
		runtime  string
		rootless bool
		uid      int
		want     []string
	}{
		{"docker", false, 1001, []string{"--user", "1001:121"}},
		{"podman", false, 1001, []string{"--user", "1001:121"}},
		{"nerdctl", false, 1001, []string{"--user", "1001:121"}},
		{"podman", true, 1001, []string{"--userns=keep-id", "--user", "1001:121"}},
		{"docker", true, 1001, []string{"--user", "0:0"}},
		{"nerdctl", true, 1001, []string{"--user", "0:0"}},
		{"docker", false, -1, nil},
	} {
		selected := &application.ContainerExecution{Rootless: tc.rootless, Environment: domain.ExecutionEnvironment{Runtime: tc.runtime}}
		if got := compatUserArgs(selected, tc.uid, 121); !slices.Equal(got, tc.want) {
			t.Errorf("%s rootless=%v: %q want %q", tc.runtime, tc.rootless, got, tc.want)
		}
	}
}

// The argv a rootless nerdctl receives: its target digest, not the config ID
// it cannot resolve, and container root rather than an unmapped host UID.
func TestRootlessNerdctlRunArguments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WARDEN_TEST_CONTAINER_ROOTLESS", "true")
	argsFile := filepath.Join(dir, "args")
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", argsFile)
	fakeContainerEngine(t, dir, "nerdctl", "0", testImageJSON)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "nerdctl", Image: "checks:dev"})
	if err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Container: selected}
	if _, _, err := runContainer(context.Background(), sc, sc.WorktreeDir, "true"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\x00")
	user := slices.Index(args, "--user")
	if user < 0 || args[user+1] != "0:0" || !slices.Contains(args, testTargetDigest) || slices.Contains(args, testImageID) || !slices.Contains(args, "--network=none") {
		t.Fatalf("rootless nerdctl argv: %q", args)
	}
}

func TestContainerRunRequiresReference(t *testing.T) {
	sc := application.StepContext{WorktreeDir: t.TempDir(), Container: &application.ContainerExecution{Binary: "/nonexistent", Environment: domain.ExecutionEnvironment{Runtime: "docker", ImageID: testImageID, Platform: "linux/arm64", Network: "none"}}}
	if _, _, err := runContainer(context.Background(), sc, sc.WorktreeDir, "true"); err == nil {
		t.Fatal("container ran without an inspected reference")
	}
}
