package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

const appleIndex = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// This matches Apple's ImageResource schema, not Docker image inspection.
const appleImageJSON = `[{"configuration":{"name":"local/checks:dev","descriptor":{"digest":"` + appleIndex + `"}},"variants":[{"digest":"` + testImageID + `","platform":{"os":"linux","architecture":"arm64"},"config":{}}]}]`

func fakeAppleEngine(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI uses POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("APPLE_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("APPLE_NAME", filepath.Join(dir, "name"))
	t.Setenv("APPLE_INSPECTION", appleImageJSON)
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$APPLE_CALLS"
case "$1" in
system) exit "${APPLE_STATUS:-0}" ;;
image) printf '%s\n' "$APPLE_INSPECTION" ;;
create)
 printf '%s\000' "$@" > "$APPLE_CALLS.args"
 while [ "$#" -gt 0 ]; do
  if [ "$1" = --name ]; then printf '%s' "$2" > "$APPLE_NAME"; fi
  shift
 done
 exit "${APPLE_CREATE_STATUS:-0}" ;;
inspect)
 name=$(cat "$APPLE_NAME")
 printf '[{"configuration":{"id":"%s","image":{"descriptor":{"digest":"%s"}},"platform":{"os":"linux","architecture":"arm64"},"networks":[],"runtimeHandler":"container-runtime-linux"}}]\n' "$name" "${APPLE_CREATED_DIGEST:-` + appleIndex + `}" ;;
start)
 if [ "${APPLE_WAIT:-}" = yes ]; then exec sleep 30; fi
 echo apple-output
 exit "${APPLE_EXIT:-0}" ;;
delete)
 if [ -n "${APPLE_DELETE_WAIT:-}" ]; then exec sleep "$APPLE_DELETE_WAIT"; fi
 exit "${APPLE_DELETE_STATUS:-0}" ;;
*) exit 90 ;;
esac
`
	path := filepath.Join(dir, "container")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestAppleDiscoveryAndImageIdentity(t *testing.T) {
	fakeAppleEngine(t)
	cfg := domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev"}
	selected, err := ResolveContainer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env := selected.Environment
	if env.Runtime != "apple" || env.ImageID != testImageID || env.ImageIndexDigest != appleIndex || env.ImageIDKind != "manifest" || env.GuestOS != "linux" || env.HostOS != runtime.GOOS {
		t.Fatalf("wrong identity: %+v", selected)
	}
	record := domain.RunRecord{Environments: map[domain.StepName]domain.ExecutionEnvironment{domain.StepTest: env}}
	if !record.ContainerChecks([]domain.StepName{domain.StepTest}, testImageID) {
		t.Fatal("Apple evidence refused")
	}
	cfg.Platform = "linux/amd64"
	if _, err := ResolveContainer(cfg); err == nil {
		t.Fatal("implicitly enabled translation")
	}
	cfg.Platform = ""
	t.Setenv("APPLE_STATUS", "1")
	if _, err := ResolveContainer(cfg); err == nil {
		t.Fatal("stopped Apple service fell back to host")
	}
	t.Setenv("APPLE_STATUS", "0")
	for _, inspection := range []string{`[]`, `{}`, `[{"Id":"` + testImageID + `","Os":"linux","Architecture":"arm64"}]`, strings.Replace(appleImageJSON, "linux", "darwin", 1), strings.Replace(appleImageJSON, appleIndex, "missing", 1)} {
		t.Setenv("APPLE_INSPECTION", inspection)
		if _, err := ResolveContainer(cfg); err == nil {
			t.Fatal("invalid Apple image accepted", inspection)
		}
	}
	// On supported Mac hosts Apple is the preferred automatic backend.
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Setenv("APPLE_INSPECTION", appleImageJSON)
		cfg.Runtime = "auto"
		if selected, err := ResolveContainer(cfg); err != nil || selected.Environment.Runtime != "apple" {
			t.Fatalf("automatic selection: %+v %v", selected, err)
		}
	}
}

func TestAppleCreateInspectStartAndCleanup(t *testing.T) {
	fakeAppleEngine(t)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev"})
	if err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": "echo check"}}
	t.Setenv("WARDEN_PRIVATE_TOKEN", "must-not-forward")
	result, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepPass || result.Environment == nil {
		t.Fatalf("check: %+v %v", result, err)
	}
	calls, err := os.ReadFile(os.Getenv("APPLE_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(calls), "create\ninspect\nstart\ndelete\n") {
		t.Fatalf("wrong lifecycle: %s", calls)
	}
	args, err := os.ReadFile(os.Getenv("APPLE_CALLS") + ".args")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--network\x00none\x00", "--no-dns", "--cap-drop\x00ALL\x00", "--platform\x00linux/arm64", "type=bind,source=", "local/checks:dev", "worktree mount verification failed"} {
		if !strings.Contains(string(args), text) {
			t.Fatalf("missing %q in arguments", text)
		}
	}
	for _, text := range []string{"--pull=never", "--security-opt", "must-not-forward", "--ssh"} {
		if strings.Contains(string(args), text) {
			t.Fatalf("unexpected argument %s", text)
		}
	}
	// A moved image tag must be rejected BEFORE its command starts.
	if err := os.WriteFile(os.Getenv("APPLE_CALLS"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPLE_CREATED_DIGEST", "sha256:"+strings.Repeat("c", 64))
	result, err = NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
	if err != nil || result.Status != domain.StepFail {
		t.Fatal("changed image executed", result, err)
	}
	calls, err = os.ReadFile(os.Getenv("APPLE_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "start") || !strings.HasSuffix(string(calls), "delete\n") {
		t.Fatalf("changed image not stopped/cleaned: %s", calls)
	}
}

// Removing the container is housekeeping after the command has finished:
// whether it succeeds says nothing about the change. The verdict comes from
// the command's exit status alone, and a failed removal is reported beside it.
// It is not a blocker: a blocker says the step failed WITHOUT running, and
// this one ran.
func TestAppleCleanupFailureKeepsTheCommandsVerdict(t *testing.T) {
	fakeAppleEngine(t)
	previous := appleCleanupTimeout
	appleCleanupTimeout = 200 * time.Millisecond
	t.Cleanup(func() { appleCleanupTimeout = previous })
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, deleteStatus, deleteWait, exit string
		want                                 domain.StepStatus
	}{
		{"delete fails and the command passed", "1", "", "0", domain.StepPass},
		{"delete killed at its timeout and the command passed", "", "30", "0", domain.StepPass},
		{"delete fails and the command failed", "1", "", "1", domain.StepFail},
		{"delete killed at its timeout and the command failed", "", "30", "1", domain.StepFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APPLE_DELETE_STATUS", tc.deleteStatus)
			t.Setenv("APPLE_DELETE_WAIT", tc.deleteWait)
			t.Setenv("APPLE_EXIT", tc.exit)
			var lines []string
			sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Container: selected,
				Commands: map[string]string{"test": "echo check"}, OnOutput: func(l string) { lines = append(lines, l) }}
			result, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
			if err != nil || result.Status != tc.want || result.Blocker != domain.BlockerNone {
				t.Fatalf("got %+v %v, want status %s and no blocker", result, err, tc.want)
			}
			if result.Environment == nil {
				t.Fatal("the environment the command ran in was dropped")
			}
			var reported string
			if tc.want == domain.StepPass {
				if len(result.Findings) != 0 {
					t.Fatalf("a passing command gained findings: %+v", result.Findings)
				}
				reported = result.Summary
			} else {
				if len(result.Findings) != 1 || !strings.Contains(result.Findings[0].Message, "apple-output") {
					t.Fatalf("the command's own output is missing: %+v", result.Findings)
				}
				reported = result.Findings[0].Message
			}
			if !strings.Contains(reported, "container cleanup failed") {
				t.Fatalf("the cleanup failure was not reported: %q", reported)
			}
			if tc.want == domain.StepPass && !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "container cleanup failed") }) {
				t.Fatalf("the cleanup failure was not shown: %q", lines)
			}
		})
	}
}

func TestAppleCreatedInspectionRejectsWrongEnvironment(t *testing.T) {
	base := `[{"configuration":{"id":"check","image":{"descriptor":{"digest":"` + appleIndex + `"}},"platform":{"os":"linux","architecture":"arm64"},"networks":[],"runtimeHandler":"container-runtime-linux"}}]`
	env := domain.ExecutionEnvironment{ImageIndexDigest: appleIndex, Platform: "linux/arm64", Network: "none"}
	if err := verifyAppleCreated([]byte(base), "check", env); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[]`, strings.Replace(base, `"check"`, `"other"`, 1), strings.Replace(base, "arm64", "amd64", 1), strings.Replace(base, "container-runtime-linux", "container-runtime-macos", 1), strings.Replace(base, `"networks":[]`, `"networks":[{}]`, 1), strings.Replace(base, `"networks":[],`, ``, 1)} {
		if err := verifyAppleCreated([]byte(bad), "check", env); err == nil {
			t.Fatal("invalid environment accepted", bad)
		}
	}
	// Fixture JSON itself is independently checked rather than ignored on parse.
	var fixture any
	if err := json.Unmarshal([]byte(base), &fixture); err != nil {
		t.Fatal(err)
	}
}

func TestAppleCancellationDeletesNamedContainer(t *testing.T) {
	fakeAppleEngine(t)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev"})
	if err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Container: selected}
	t.Setenv("APPLE_WAIT", "yes")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, _, err := runContainer(ctx, sc, sc.WorktreeDir, "sleep 30"); err == nil {
		t.Fatal("cancelled run passed")
	}
	calls, err := os.ReadFile(os.Getenv("APPLE_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(calls), "delete\n") {
		t.Fatal("cancelled container was not deleted", string(calls))
	}
}
