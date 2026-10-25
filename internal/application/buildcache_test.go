package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// fakeBuildCache records how the runner holds and bounds the build cache.
type fakeBuildCache struct {
	shareErr error
	// trim, when set, runs as the eviction — the stand-in for removing the
	// cache — and its error is returned.
	trim func(dir string) error
	// events records the calls in order: "share:<dir>", "release",
	// "trim:<dir>", and "step" for each validation step executed.
	events []string
}

func (c *fakeBuildCache) Share(dir string) (func(), error) {
	if c.shareErr != nil {
		return nil, c.shareErr
	}
	c.events = append(c.events, "share:"+dir)
	return func() { c.events = append(c.events, "release") }, nil
}

func (c *fakeBuildCache) Trim(dir string) error {
	c.events = append(c.events, "trim:"+dir)
	if c.trim != nil {
		return c.trim(dir)
	}
	return nil
}

// contextFactory is fakeFactory that also captures the StepContext the
// kernel was built with.
type contextFactory struct {
	kernel *fakeKernel
	sc     *StepContext
}

func (f *contextFactory) New(_ domain.ResolvedPolicy, sc StepContext, _ *[]domain.Finding, push PushFunc) (Kernel, error) {
	f.kernel.push = push
	*f.sc = sc
	return f.kernel, nil
}

// TestRunner_BuildCacheHeldWhileStepsRun pins the ordering the eviction's
// safety depends on: the shared hold is taken before the first step and
// released after the last, and the trim happens only once the hold is gone —
// otherwise a run would find its own cache busy and never bound it.
func TestRunner_BuildCacheHeldWhileStepsRun(t *testing.T) {
	gitDir := t.TempDir()
	cacheDir := BuildCacheDir(gitDir)
	bc := &fakeBuildCache{}
	git := &fakeGit{gitDir: gitDir, root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
	kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
	kernel.during = func(domain.StepName) { bc.events = append(bc.events, "step") }
	r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())
	r.BuildCache = bc

	if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
		t.Fatal(err)
	}
	want := []string{"share:" + cacheDir, "step", "step", "release", "trim:" + cacheDir}
	if !reflect.DeepEqual(bc.events, want) {
		t.Errorf("events = %v, want %v", bc.events, want)
	}
}

// TestRunner_BuildCacheUnheldIsNotUsed pins the fallback: a cache the run
// cannot hold is not handed to the steps, so they build cold rather than in a
// directory a concurrent eviction could remove underneath them.
func TestRunner_BuildCacheUnheldIsNotUsed(t *testing.T) {
	gitDir := t.TempDir()
	tests := []struct {
		name string
		bc   BuildCache
		want string
	}{
		{name: "held", bc: &fakeBuildCache{}, want: BuildCacheDir(gitDir)},
		{name: "hold refused", bc: &fakeBuildCache{shareErr: errors.New("no locks here")}, want: ""},
		{name: "no guard wired", bc: nil, want: BuildCacheDir(gitDir)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &fakeGit{gitDir: gitDir, root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
			kernel := &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}
			r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())
			var sc StepContext
			r.Kernels = &contextFactory{kernel: kernel, sc: &sc}
			r.BuildCache = tt.bc
			res, err := r.Run(context.Background(), domain.PrePush)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != domain.OutcomePassed {
				t.Errorf("outcome = %s, want passed: the cache is an optimization", res.Outcome)
			}
			if sc.BuildCacheDir != tt.want {
				t.Errorf("steps got BuildCacheDir %q, want %q", sc.BuildCacheDir, tt.want)
			}
		})
	}
}

// TestRunner_EvictionNeverChangesVerdict runs the same change with no cache
// guard, with an eviction that removes the cache, and with one that fails, and
// requires the identical result each time — for a passing and a failing run.
// The eviction is after the verdict by construction; this pins that nobody
// later threads its outcome back into the result.
func TestRunner_EvictionNeverChangesVerdict(t *testing.T) {
	outcomes := map[string]map[domain.StepName]domain.StepStatus{
		"passing": {},
		"failing": {domain.StepLint: domain.StepFail},
	}
	evictions := map[string]func() BuildCache{
		"removes the cache": func() BuildCache {
			return &fakeBuildCache{trim: func(dir string) error { return os.RemoveAll(dir) }}
		},
		"fails": func() BuildCache {
			return &fakeBuildCache{trim: func(string) error { return errors.New("disk on fire") }}
		},
		"cache busy": func() BuildCache {
			return &fakeBuildCache{trim: func(string) error { return nil }}
		},
	}
	runOnce := func(t *testing.T, status map[domain.StepName]domain.StepStatus, bc BuildCache) RunResult {
		t.Helper()
		gitDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(BuildCacheDir(gitDir), "cargo-target"), 0o755); err != nil {
			t.Fatal(err)
		}
		git := &fakeGit{gitDir: gitDir, root: "/repo", branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
		kernel := &fakeKernel{outcomes: status}
		r := newRunner(t, git, kernel, fakeApprover{approve: true}, prePushCfg())
		r.BuildCache = bc
		res, err := r.Run(context.Background(), domain.PrePush)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for name, status := range outcomes {
		baseline := runOnce(t, status, nil)
		for evName, newCache := range evictions {
			t.Run(name+"/"+evName, func(t *testing.T) {
				got := runOnce(t, status, newCache())
				if !reflect.DeepEqual(got, baseline) {
					t.Errorf("result changed by the eviction:\n got  %+v\n want %+v", got, baseline)
				}
			})
		}
	}
}

// TestRunner_BuildCacheBoundedOnEveryEntryPoint covers the entry points other
// than Run: a watcher that only ever predicts must bound the cache too, or the
// cache it fills grows without limit.
func TestRunner_BuildCacheBoundedOnEveryEntryPoint(t *testing.T) {
	gitDir := t.TempDir()
	t.Run("PredictStaged", func(t *testing.T) {
		bc := &fakeBuildCache{}
		git := &fakeGit{gitDir: gitDir, root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
		r := newRunner(t, git, &fakeKernel{}, fakeApprover{approve: true}, prePushCfg())
		r.BuildCache = bc
		_, _ = r.PredictStaged(context.Background()) // no predictable steps: an error, still bounded
		if len(bc.events) == 0 || bc.events[len(bc.events)-1] != "trim:"+BuildCacheDir(gitDir) {
			t.Errorf("events = %v, want a trim last", bc.events)
		}
	})
	t.Run("CheckWorkingTree", func(t *testing.T) {
		bc := &fakeBuildCache{}
		git := &fakeGit{gitDir: gitDir, root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
		r := newRunner(t, git, &fakeKernel{}, fakeApprover{approve: true}, prePushCfg())
		r.BuildCache = bc
		_, _ = r.CheckWorkingTree(context.Background(), "not-a-sha", "not-a-sha")
		if len(bc.events) == 0 || bc.events[len(bc.events)-1] != "trim:"+BuildCacheDir(gitDir) {
			t.Errorf("events = %v, want a trim last", bc.events)
		}
	})
	t.Run("no git dir, no trim", func(t *testing.T) {
		bc := &fakeBuildCache{}
		git := &fakeGit{root: t.TempDir(), branch: "main", head: "sha1", wt: &fakeWorktree{dir: "/wt", headSHA: "sha1"}}
		r := newRunner(t, git, &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}, fakeApprover{approve: true}, prePushCfg())
		r.BuildCache = bc
		if _, err := r.Run(context.Background(), domain.PrePush); err != nil {
			t.Fatal(err)
		}
		if len(bc.events) != 0 {
			t.Errorf("events = %v, want none without a cache directory", bc.events)
		}
	})
}
