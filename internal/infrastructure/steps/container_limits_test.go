package steps

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func TestCompatLimitArgs(t *testing.T) {
	cases := []struct {
		limits domain.ExecutionLimits
		want   []string
	}{
		{domain.ExecutionLimits{}, nil},
		{domain.ExecutionLimits{Memory: "4GiB", CPUs: "1.5", Pids: 256}, []string{"--memory=4096m", "--memory-swap=4096m", "--cpus=1.5", "--pids-limit=256"}},
		{domain.ExecutionLimits{CPUs: "0.25"}, []string{"--cpus=0.25"}},
		{domain.ExecutionLimits{Memory: "512MiB"}, []string{"--memory=512m", "--memory-swap=512m"}},
	}
	for _, c := range cases {
		got, err := compatLimitArgs(c.limits)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("compatLimitArgs(%+v) = %q, %v; want %q", c.limits, got, err, c.want)
		}
	}
	if _, err := compatLimitArgs(domain.ExecutionLimits{Memory: "4G"}); err == nil {
		t.Error("an invalid memory limit produced flags")
	}
}

func TestAppleLimitArgs(t *testing.T) {
	got, err := appleLimitArgs(&domain.ExecutionLimits{Memory: "2GiB", CPUs: "4"})
	if err != nil || !slices.Equal(got, []string{"--memory", "2048M", "--cpus", "4"}) {
		t.Fatalf("appleLimitArgs = %q, %v", got, err)
	}
	if got, err := appleLimitArgs(nil); got != nil || err != nil {
		t.Fatalf("no limits must add no flags: %q %v", got, err)
	}
	for _, l := range []domain.ExecutionLimits{{Pids: 64}, {CPUs: "1.5"}} {
		if _, err := appleLimitArgs(&l); err == nil {
			t.Errorf("apple cannot apply %+v, and must say so", l)
		}
	}
}

// A runtime that cannot apply a requested limit is not selected: with runtime
// auto, selection moves on rather than run the check unbounded, and with apple
// named explicitly it fails naming the limit.
func TestResolveContainer_RefusesARuntimeThatCannotApplyTheLimits(t *testing.T) {
	fakeAppleEngine(t)
	if _, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev", Limits: &domain.ExecutionLimits{Pids: 64}}); err == nil || !strings.Contains(err.Error(), "execution.limits.pids") {
		t.Fatalf("apple with a pids limit must be refused naming it, got %v", err)
	}
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "apple", Image: "checks:dev", Limits: &domain.ExecutionLimits{Memory: "1GiB", CPUs: "2"}})
	if err != nil || selected.Limits != (domain.ExecutionLimits{Memory: "1GiB", CPUs: "2"}) {
		t.Fatalf("apple with limits it can apply: %+v %v", selected, err)
	}
}

// The limits reach the engine's run arguments.
func TestShellContainerCarriesLimitFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", argsFile)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev", Limits: &domain.ExecutionLimits{Memory: "1GiB", CPUs: "2", Pids: 128}})
	if err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": "true"}}
	if _, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(raw), "\x00")
	for _, want := range []string{"--memory=1024m", "--memory-swap=1024m", "--cpus=2", "--pids-limit=128"} {
		if !slices.Contains(args, want) {
			t.Errorf("run args lack %s: %q", want, args)
		}
	}
}

// Against a real engine, the workload's own cgroup holds the requested bounds.
// This is the fact ADR 0008 builds on; it runs in the container CI jobs.
func TestContainerLimitsReachTheCgroup(t *testing.T) {
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	runtimeName := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if runtimeName == "" {
		runtimeName = "docker"
	}
	if runtimeName == "apple" {
		t.Skip("apple sizes a VM; its in-VM view is not settled (ADR 0008 open question 2)")
	}
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: runtimeName, Image: image, Limits: &domain.ExecutionLimits{Memory: "512MiB", CPUs: "1.5", Pids: 128}})
	if err != nil {
		t.Fatal(err)
	}
	selected.RequireObservedBounds = true
	dir := t.TempDir()
	sc := application.StepContext{WorktreeDir: dir, Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": `cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/cpu.max /sys/fs/cgroup/pids.max > observed-limits`}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if res, err := NewShellStep(domain.StepTest, "test").Run(ctx, sc); err != nil || res.Status != domain.StepPass {
		t.Fatalf("result: %+v %v", res, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "observed-limits"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"536870912", "150000 100000", "128"}
	if !slices.Equal(lines, want) {
		t.Fatalf("cgroup inside the container reads %q, want %q (an engine that ignores limits must be visible here)", lines, want)
	}
	// warden's own probe saw the same bounds, and the network none it ran in.
	res, err := NewShellStep(domain.StepTest, "test").Run(ctx, application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": "true"}})
	if err != nil || res.Status != domain.StepPass || res.Observed == nil {
		t.Fatalf("no observation from a real engine: %+v %v", res, err)
	}
	if short := res.Observed.Shortfalls(selected.Limits, "none"); len(short) != 0 {
		t.Fatalf("the probe saw shortfalls on a real engine: %q (observation %+v)", short, *res.Observed)
	}
}

// What the workload observed reaches the step result, and a shortfall against
// the request is reported in the summary and live output without changing the
// verdict. An engine that ignored the limits looks exactly like this.
func TestShellContainerReportsObservedShortfalls(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	t.Setenv("WARDEN_TEST_CONTAINER_ARGS", filepath.Join(t.TempDir(), "args"))
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev", Limits: &domain.ExecutionLimits{Memory: "512MiB", Pids: 128}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, observed string
		wantShort      []string
	}{
		{"held", `cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\npids.max=128\nroutes=0\nroutes6=0\n`, nil},
		{"engine ignored the limits", `cgroup=v2\nmemory.max=max\nmemory.swap.max=max\npids.max=max\nroutes=0\nroutes6=0\n`, []string{"memory: observed unbounded", "pids: observed unbounded"}},
		{"probe left nothing", "", []string{"memory: not observed", "network none: routes not observed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WARDEN_TEST_OBSERVED", c.observed)
			var lines []string
			worktree := t.TempDir()
			sc := application.StepContext{WorktreeDir: worktree, Hook: domain.PreCommit, Container: selected, Commands: map[string]string{"test": "true"}, OnOutput: func(l string) { lines = append(lines, l) }}
			res, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
			if err != nil || res.Status != domain.StepPass {
				t.Fatalf("a shortfall must not change the verdict: %+v %v", res, err)
			}
			if c.observed != "" && (res.Observed == nil || res.Observed.Cgroup != "v2") {
				t.Fatalf("observation not carried: %+v", res.Observed)
			}
			for _, want := range c.wantShort {
				if !strings.Contains(res.Summary, want) || !strings.Contains(strings.Join(lines, "\n"), want) {
					t.Errorf("summary %q / output %q lack %q", res.Summary, lines, want)
				}
			}
			if c.wantShort == nil && strings.Contains(res.Summary, "not observed as requested") {
				t.Errorf("bounds that held reported a shortfall: %q", res.Summary)
			}
			if entries, _ := os.ReadDir(worktree); len(entries) != 0 {
				t.Errorf("probe files left in the worktree: %v", entries)
			}
		})
	}
}

func TestShellStrictObservedBoundsRejectsMissingAndLooserEvidence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fakeContainerEngine(t, dir, "docker", "0", testImageJSON)
	selected, err := ResolveContainer(domain.ExecutionConfig{Runtime: "docker", Image: "checks:dev", Limits: &domain.ExecutionLimits{Memory: "512MiB", CPUs: "1.5", Pids: 128}})
	if err != nil {
		t.Fatal(err)
	}
	selected.RequireObservedBounds = true
	valid := `cgroup=v2\nmemory.max=536870912\nmemory.swap.max=0\ncpu.max=150000 100000\npids.max=128\nroutes=0\nroutes6=0\n`
	for _, tc := range []struct {
		name, raw string
		want      domain.StepStatus
	}{
		{"held", valid, domain.StepPass}, {"missing", "", domain.StepFail},
		{"looser", strings.Replace(valid, "536870912", "1073741824", 1), domain.StepFail},
		{"unbounded", strings.Replace(valid, "128", "max", 1), domain.StepFail},
		{"routed", strings.Replace(valid, "routes6=0", "routes6=1", 1), domain.StepFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WARDEN_TEST_OBSERVED", tc.raw)
			sc := application.StepContext{WorktreeDir: t.TempDir(), Hook: domain.PrePush, Container: selected, Commands: map[string]string{"test": "true"}}
			res, err := NewShellStep(domain.StepTest, "test").Run(context.Background(), sc)
			if err != nil || res.Status != tc.want {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if tc.want == domain.StepFail && res.Contract != nil {
				t.Fatal("failed observation created reusable success contract")
			}
		})
	}
}
