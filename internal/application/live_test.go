package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// snapshotGit is a fakeGit that can seed dependency-free snapshot worktrees,
// recording what it was asked for.
type snapshotGit struct {
	*fakeGit
	seeded    [][2]string
	diffed    [][2]string
	seedErr   error
	snapPaths []string
}

func (g *snapshotGit) SeedDependencyFreeWorktree(domain.Hook, string) (Worktree, error) {
	return nil, errors.New("live checks must seed from the snapshot, not the index")
}

func (g *snapshotGit) SeedDependencyFreeSnapshot(base, tree string) (Worktree, error) {
	g.seeded = append(g.seeded, [2]string{base, tree})
	if g.seedErr != nil {
		return nil, g.seedErr
	}
	return g.wt, nil
}

func (g *snapshotGit) SnapshotDiffStats(base, tree string) (domain.DiffStats, error) {
	g.diffed = append(g.diffed, [2]string{base, tree})
	return domain.DiffStats{FilesTouched: len(g.snapPaths), Paths: g.snapPaths}, nil
}

// recordingFactory captures the policy and step context a kernel is built
// with.
type recordingFactory struct {
	kernel *fakeKernel
	policy domain.ResolvedPolicy
	sc     StepContext
	push   bool
}

func (f *recordingFactory) New(p domain.ResolvedPolicy, sc StepContext, _ *[]domain.Finding, push PushFunc) (Kernel, error) {
	f.policy, f.sc, f.push = p, sc, push != nil
	f.kernel.push = push
	return f.kernel, nil
}

var (
	liveBase = strings.Repeat("b", 40)
	liveTree = strings.Repeat("e", 40)
)

func liveConfig() domain.Config {
	return domain.Config{
		Execution:       domain.ExecutionConfig{Image: "checks:dev"},
		PredictiveCache: []string{"test", "custom"},
		Rules:           []domain.Rule{{Match: domain.Match{Branch: "feat/x"}, Then: domain.Then{AutoFix: map[domain.StepName]int{"fixer": 2}}}},
		Steps:           map[string][]domain.StepName{"pre_commit": {"lint", "test", "review", "security-scan", "custom", "fixer"}},
		Commands: map[string]string{
			"lint": "make lint", "test": "make test", "review": "true",
			"security-scan": "nox scan", "custom": "./check", "fixer": "fmt -w",
		},
	}
}

func newLiveRunner(t *testing.T, cfg domain.Config) (*Runner, *snapshotGit, *recordingFactory) {
	t.Helper()
	git := &snapshotGit{
		fakeGit:   &fakeGit{branch: "feat/x", head: liveBase, wt: &fakeWorktree{dir: "/wt", headSHA: liveBase, diffSince: "diff --git a/f b/f\n"}},
		snapPaths: []string{"f", "new"},
	}
	factory := &recordingFactory{kernel: &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}}}
	r := newRunner(t, git.fakeGit, factory.kernel, fakeApprover{approve: true}, cfg)
	r.Git = git
	r.Kernels = factory
	return r, git, factory
}

// A live check runs only container shell checks, against the snapshot it was
// given, with no prediction cache and no step authorized to fix; it computes
// no fix patch, writes no note and pushes nothing.
func TestCheckWorkingTreeRunsOnlyAdvisoryShellChecks(t *testing.T) {
	r, git, factory := newLiveRunner(t, liveConfig())
	res, err := r.CheckWorkingTree(context.Background(), liveBase, liveTree)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != domain.OutcomePassed || res.Hook != domain.PreCommit {
		t.Fatalf("result %+v", res)
	}
	if want := []domain.StepName{"lint", "test", "custom"}; !slices.Equal(factory.policy.Steps, want) {
		t.Fatalf("steps %v, want %v", factory.policy.Steps, want)
	}
	if !slices.Equal(factory.kernel.ran, factory.policy.Steps) {
		t.Fatalf("ran %v", factory.kernel.ran)
	}
	for _, s := range factory.policy.Steps {
		if factory.policy.Predictable(s) || factory.policy.AutoFixBudget(s) != 0 {
			t.Fatalf("%s may use the prediction cache or fix", s)
		}
	}
	if factory.policy.AuthorizesFix() || len(factory.policy.PredictiveCache) != 0 {
		t.Fatalf("policy %+v", factory.policy)
	}
	if factory.push || factory.sc.Hook != domain.PreCommit || factory.sc.WorktreeDir != "/wt" || !slices.Equal(factory.sc.Diff.Paths, git.snapPaths) {
		t.Fatalf("step context %+v push %v", factory.sc, factory.push)
	}
	if len(git.seeded) != 1 || git.seeded[0] != [2]string{liveBase, liveTree} || len(git.diffed) != 1 || git.diffed[0] != git.seeded[0] {
		t.Fatalf("seeded %v diffed %v", git.seeded, git.diffed)
	}
	if res.FixPatch != "" || !git.wt.removed || git.wroteNote || git.pushed || res.Record != nil {
		t.Fatalf("patch %q removed %v note %v pushed %v record %v", res.FixPatch, git.wt.removed, git.wroteNote, git.pushed, res.Record)
	}
}

func TestCheckWorkingTreeReportsAFailingCheck(t *testing.T) {
	r, git, factory := newLiveRunner(t, liveConfig())
	factory.kernel.outcomes[domain.StepTest] = domain.StepFail
	res, err := r.CheckWorkingTree(context.Background(), liveBase, liveTree)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == domain.OutcomePassed || res.FixPatch != "" || !git.wt.removed {
		t.Fatalf("result %+v removed %v", res, git.wt.removed)
	}
}

func TestCheckWorkingTreeRefusals(t *testing.T) {
	host := liveConfig()
	host.Execution = domain.ExecutionConfig{}
	none := liveConfig()
	none.Steps = map[string][]domain.StepName{"pre_commit": {"review", "fixer"}}
	for _, tc := range []struct {
		name       string
		cfg        domain.Config
		base, tree string
		plainGit   bool
		seedErr    error
		want       string
	}{
		{name: "no commit", cfg: liveConfig(), tree: liveTree, want: "first commit"},
		{name: "not a tree", cfg: liveConfig(), base: liveBase, tree: "HEAD", want: "not a tree"},
		{name: "host execution", cfg: host, base: liveBase, tree: liveTree, want: "container execution"},
		{name: "no shell checks", cfg: none, base: liveBase, tree: liveTree, want: ErrNoLiveChecks.Error()},
		{name: "git cannot seed", cfg: liveConfig(), base: liveBase, tree: liveTree, plainGit: true, want: "dependency-free"},
		{name: "seed fails", cfg: liveConfig(), base: liveBase, tree: liveTree, seedErr: errors.New("disk full"), want: "disk full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, git, factory := newLiveRunner(t, tc.cfg)
			git.seedErr = tc.seedErr
			if tc.plainGit {
				r.Git = git.fakeGit
			}
			_, err := r.CheckWorkingTree(context.Background(), tc.base, tc.tree)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
			if len(factory.kernel.ran) != 0 {
				t.Fatalf("ran %v after a refusal", factory.kernel.ran)
			}
		})
	}
}
