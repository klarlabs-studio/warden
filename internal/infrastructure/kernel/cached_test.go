package kernel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// fakeCache is a scripted StepCache: fp is the fingerprint it returns, seen
// controls a hit, and recorded captures keys written.
type fakeCache struct {
	fp       string
	seen     bool
	recorded []string
}

func (c *fakeCache) Fingerprint(string, []string) string { return c.fp }
func (c *fakeCache) Seen(string) bool                    { return c.seen }
func (c *fakeCache) Record(key string)                   { c.recorded = append(c.recorded, key) }

func TestCachedStep_HitSkipsInner(t *testing.T) {
	inner := &fakeStep{name: domain.StepTest, status: domain.StepPass}
	cache := &fakeCache{fp: "fp1", seen: true}
	cs := cachedStep{inner: inner, cache: cache, globs: []string{"**/*.go"}, command: "go test"}

	res, err := cs.Run(context.Background(), application.StepContext{})
	if err != nil {
		t.Fatal(err)
	}
	if inner.ran {
		t.Error("a cache hit must not run the underlying step")
	}
	if res.Status != domain.StepPass || res.Summary == "" {
		t.Errorf("cache hit should be a pass with a summary, got %+v", res)
	}
	if res.Outcome != domain.StepCached || res.OutcomeReason != "inputs unchanged" {
		t.Errorf("a cache hit must be marked cached, not executed: %+v", res)
	}
}

func TestCachedStep_MissIsMarkedExecuted(t *testing.T) {
	inner := &fakeStep{name: domain.StepTest, status: domain.StepPass}
	cs := cachedStep{inner: inner, cache: &fakeCache{fp: "fp1"}, globs: []string{"**/*.go"}, command: "go test"}
	res, err := cs.Run(context.Background(), application.StepContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExecutionOutcome() != domain.StepExecuted {
		t.Errorf("a miss ran the step and must read as executed, got %q", res.ExecutionOutcome())
	}
}

// skippingStep passes without executing, the way an agent step with no agent
// configured does.
type skippingStep struct{ name domain.StepName }

func (s skippingStep) Name() domain.StepName { return s.name }
func (s skippingStep) Run(context.Context, application.StepContext) (domain.StepResult, error) {
	return domain.StepResult{Step: s.name, Status: domain.StepPass, Outcome: domain.StepSkipped, OutcomeReason: "no agent configured"}, nil
}

// A skipped pass must not be cached: the next run would replay it as
// "cached — inputs unchanged", which reads as though the work was done once.
func TestCachedStep_SkippedPassNotRecorded(t *testing.T) {
	cache := &fakeCache{fp: "fp1"}
	cs := cachedStep{inner: skippingStep{name: domain.StepReview}, cache: cache, globs: []string{"**/*"}, command: "\x00"}
	res, err := cs.Run(context.Background(), application.StepContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.StepSkipped {
		t.Errorf("the inner skip must pass through unchanged, got %+v", res)
	}
	if len(cache.recorded) != 0 {
		t.Errorf("a skipped pass must not be cached, recorded %d", len(cache.recorded))
	}
}

// The marker is a delivery-surface fact. Evidence feeds the signed chain, so a
// skipped or cached pass must produce exactly the evidence an executed pass
// does — otherwise this change would alter the note for an unchanged run.
func TestEvidenceFor_OutcomeMarkerStaysOutOfEvidence(t *testing.T) {
	plain := domain.StepResult{Step: domain.StepReview, Status: domain.StepPass}
	want := evidenceFor(plain)
	for _, o := range []domain.StepOutcome{domain.StepSkipped, domain.StepCached} {
		marked := plain
		marked.Outcome, marked.OutcomeReason = o, "reason"
		if got := evidenceFor(marked); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: evidence changed:\n got %+v\nwant %+v", o, got, want)
		}
	}
}

func TestCachedStep_MissRunsAndRecords(t *testing.T) {
	inner := &fakeStep{name: domain.StepTest, status: domain.StepPass}
	cache := &fakeCache{fp: "fp1", seen: false}
	cs := cachedStep{inner: inner, cache: cache, globs: []string{"**/*.go"}, command: "go test"}

	if _, err := cs.Run(context.Background(), application.StepContext{}); err != nil {
		t.Fatal(err)
	}
	if !inner.ran {
		t.Error("a cache miss must run the underlying step")
	}
	if len(cache.recorded) != 1 {
		t.Errorf("a passing miss must record one key, got %d", len(cache.recorded))
	}
}

func TestCachedStep_FailingRunNotRecorded(t *testing.T) {
	inner := &fakeStep{name: domain.StepLint, status: domain.StepFail}
	cache := &fakeCache{fp: "fp1", seen: false}
	cs := cachedStep{inner: inner, cache: cache, globs: []string{"**/*.go"}, command: "lint"}

	if _, err := cs.Run(context.Background(), application.StepContext{}); err != nil {
		t.Fatal(err)
	}
	if len(cache.recorded) != 0 {
		t.Error("a failing step must not be cached")
	}
}

func TestCachedStep_NoFingerprintAlwaysRuns(t *testing.T) {
	inner := &fakeStep{name: domain.StepTest, status: domain.StepPass}
	cache := &fakeCache{fp: "", seen: true} // seen=true but no fingerprint → can't key
	cs := cachedStep{inner: inner, cache: cache, globs: []string{"**/*.rs"}, command: "go test"}

	if _, err := cs.Run(context.Background(), application.StepContext{}); err != nil {
		t.Fatal(err)
	}
	if !inner.ran {
		t.Error("without a fingerprint the step must run (no false hit)")
	}
	if len(cache.recorded) != 0 {
		t.Error("without a fingerprint nothing is recorded")
	}
}

func TestContainerRunCannotReuseHostStepCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake engine uses POSIX shell")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = info ]; then echo '[\"name=seccomp,profile=builtin\"]'; exit 0; fi\nprintf '%s\\n' '[{\"Id\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"Os\":\"linux\",\"Architecture\":\"arm64\"}]'\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	inner := &fakeStep{name: domain.StepTest, status: domain.StepPass}
	cache := &fakeCache{fp: "same-inputs", seen: true}
	policy := policyFor(domain.StepTest)
	policy.Execution = domain.ExecutionConfig{Image: "checks:dev"}
	policy.Cache = map[domain.StepName][]string{domain.StepTest: {"**"}}
	factory := newFactory(inner).WithCache(cache)
	kernel, err := factory.New(policy, application.StepContext{}, new([]domain.Finding), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Execute(context.Background(), domain.StepTest); err != nil {
		t.Fatal(err)
	}
	if !inner.ran || len(cache.recorded) != 0 {
		t.Fatal("container run reused or populated host cache")
	}
}
