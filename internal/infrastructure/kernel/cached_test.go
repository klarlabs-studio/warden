package kernel

import (
	"context"
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
