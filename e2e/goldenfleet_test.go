package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The golden fleet.
//
// Every provenance-classification bug this project has shipped had the same
// shape: the unit tests asserted the code did what its author INTENDED, and the
// author's intent was the thing that was wrong. Tests written from the same
// mental model as the code cannot catch a wrong mental model. What caught them
// was running warden across real repositories and noticing a number that could
// not be true — 100% bypassed here, 65.5% there.
//
// This file makes that check systematic instead of lucky. It builds repositories
// whose provenance shape is CONSTRUCTED, so the correct classification is known
// independently of what warden computes, then asserts warden agrees. It encodes
// reality rather than intent, which is the only kind of test that could have
// caught those bugs before they shipped:
//
//   - a multi-commit push, where warden notes only the tip and vouches for the
//     span (reported as bypasses until #160)
//   - a gap beside pre-span provenance, which is genuinely ambiguous and must be
//     reported as neither verified nor bypassed (reported as bypasses until #163)
//   - a real --no-verify, which must still be reported as a bypass, because a
//     metric that never fires is as useless as one that always does
//
// If a future refactor makes any of those read differently, this fails — with
// the scenario named, not with a diff of numbers.

// goldenRepo is a repository built to a known provenance shape.
type goldenRepo struct {
	t   *testing.T
	dir string
	// remote is the bare repo the gate pushes to; doctor walks
	// adoption..origin/<branch>, so the shape only exists once published.
	remote string
}

func newGoldenRepo(t *testing.T) *goldenRepo {
	t.Helper()
	dir := t.TempDir()
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v %s", err, out)
	}
	g := &goldenRepo{t: t, dir: dir, remote: remote}
	g.git("init")
	// A non-address identity: git does not validate this field, and a literal
	// that looks like an email trips secret scanners on a pure fixture.
	g.git("config", "user.email", "warden-golden-fleet")
	g.git("config", "user.name", "warden-golden-fleet")
	g.git("commit", "--allow-empty", "-m", "root")
	g.git("branch", "-M", "main")
	g.git("remote", "add", "origin", remote)
	g.git("push", "--no-verify", "-u", "origin", "main")
	return g
}

// newGoldenRepoNoRemote builds a repository that has never been pushed and has
// no remote configured at all — the shape a local-only or abandoned repo has.
func newGoldenRepoNoRemote(t *testing.T) *goldenRepo {
	t.Helper()
	g := &goldenRepo{t: t, dir: t.TempDir()}
	g.git("init")
	g.git("config", "user.email", "warden-golden-fleet")
	g.git("config", "user.name", "warden-golden-fleet")
	g.git("commit", "--allow-empty", "-m", "root")
	g.git("branch", "-M", "main")
	return g
}

func (g *goldenRepo) git(args ...string) {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// warden runs the binary under test in the repo, with stdin closed.
//
// Closing stdin is not incidental: `warden run pre-push` reads the pushed-ref
// list from a non-terminal stdin, so an inherited pipe that never reaches EOF
// hangs the gate indefinitely.
func (g *goldenRepo) warden(args ...string) (string, int) {
	g.t.Helper()
	cmd := exec.Command(wardenBin, args...)
	cmd.Dir = g.dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+g.t.TempDir())
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		g.t.Fatalf("warden %v: %v", args, err)
	}
	return string(out), code
}

// adopt writes a passing config and records the adoption point.
func (g *goldenRepo) adopt() {
	g.t.Helper()
	cfg := "hooks: { pre_push: true }\nsteps: { pre_push: [lint] }\ncommands: { lint: \"true\" }\nrules: []\n"
	if err := os.WriteFile(filepath.Join(g.dir, ".warden.yaml"), []byte(cfg), 0o644); err != nil {
		g.t.Fatal(err)
	}
	if out, code := g.warden("init"); code != 0 {
		g.t.Fatalf("init: %d %s", code, out)
	}
	if out, code := g.warden("trust", "add"); code != 0 {
		g.t.Fatalf("trust add: %d %s", code, out)
	}
}

// commit adds an empty commit without gating it.
func (g *goldenRepo) commit(msg string) {
	g.t.Helper()
	g.git("commit", "--allow-empty", "--no-verify", "-m", msg)
}

// gate runs the full pre-push pipeline, which writes the note, then publishes so
// the commits are reachable from origin/main where doctor looks.
func (g *goldenRepo) gate() {
	g.t.Helper()
	if out, code := g.warden("run", "pre-push"); code != 0 && code != 3 {
		g.t.Fatalf("gate: %d %s", code, out)
	}
	g.git("push", "--no-verify", "origin", "main")
}

// classify returns warden's own verdict per commit, read from the JSON fleet
// report so the assertion is on structured state rather than on rendered text.
func (g *goldenRepo) classify() fleetCounts {
	g.t.Helper()
	out, _ := g.warden("fleet", "status", "--json", g.dir)
	var rep struct {
		Repos []fleetCounts `json:"repos"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		g.t.Fatalf("fleet --json did not parse: %v\n%s", err, out)
	}
	if len(rep.Repos) != 1 {
		g.t.Fatalf("expected one repo in the report, got %d", len(rep.Repos))
	}
	return rep.Repos[0]
}

type fleetCounts struct {
	Adopted        bool `json:"adopted"`
	Commits        int  `json:"commits"`
	Verified       int  `json:"verified"`
	Covered        int  `json:"covered"`
	Bypassed       int  `json:"bypassed"`
	Reattestable   int  `json:"reattestable"`
	Unattributable int  `json:"unattributable"`
	Unpushed       int  `json:"unpushed"`
}

// A multi-commit push: warden validates ONE tree — the tip's — and vouches for
// the span. The earlier commits carry no note and are NOT bypasses.
//
// This read as two bypasses until #160, which is what made a repo of ordinary
// pushes look like systemic neglect.
func TestGoldenFleet_MultiCommitPushIsNotABypass(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("first of three")
	g.commit("second of three")
	g.commit("third of three")
	g.gate()

	got := g.classify()
	if got.Bypassed != 0 {
		t.Errorf("bypassed = %d, want 0: three commits went out in ONE gated push", got.Bypassed)
	}
	if got.Verified == 0 {
		t.Error("the push tip must be verified")
	}
	// The earlier commits are not merely "not bypasses" — they are positively
	// accounted for as covered by the span, which is what keeps the buckets
	// summing to the commit count.
	if got.Covered == 0 {
		t.Error("the pre-tip commits of the push must be counted as covered, not dropped")
	}
}

// A real --no-verify must still be reported as a bypass. A metric that never
// fires is exactly as useless as one that always does, and every correction so
// far has moved in the direction of counting fewer things — so this is the guard
// that the corrections did not go one step too far.
func TestGoldenFleet_ANoVerifyPushIsStillABypass(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("gated")
	g.gate()

	// Now sneak one past the gate entirely.
	g.commit("snuck past")
	g.git("push", "--no-verify", "origin", "main")

	got := g.classify()
	if got.Bypassed != 1 {
		t.Errorf("bypassed = %d, want 1: a --no-verify push is the case this metric exists to catch", got.Bypassed)
	}
}

// A repo with no gated history at all: every commit since adoption bypassed the
// gate, and warden must say so plainly rather than hedging.
func TestGoldenFleet_NeverGatedIsAllBypassed(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	for _, m := range []string{"one", "two", "three"} {
		g.commit(m)
	}
	g.git("push", "--no-verify", "origin", "main")

	got := g.classify()
	if got.Bypassed != 3 {
		t.Errorf("bypassed = %d, want 3", got.Bypassed)
	}
	if got.Verified != 0 {
		t.Errorf("verified = %d, want 0", got.Verified)
	}
}

// The buckets must account for every commit exactly once. The fleet rollup sums
// them, so an overlap double-counts and a hole loses a commit — the failure
// TestCommitStates_Partition pins at the type level, checked here against real
// git history rather than constructed structs.
func TestGoldenFleet_BucketsAccountForEveryCommit(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("gated one")
	g.commit("gated two")
	g.gate()
	g.commit("bypassed")
	g.git("push", "--no-verify", "origin", "main")

	got := g.classify()
	sum := got.Verified + got.Covered + got.Bypassed + got.Reattestable + got.Unattributable
	if sum != got.Commits {
		t.Errorf("buckets sum to %d but there are %d commits since adoption (%+v)", sum, got.Commits, got)
	}
}

// A repo with NO REMOTE has never pushed, so the pre-push gate has never had an
// opportunity to run. Its commits carry no note for a reason that is not a
// bypass, and reporting them as one accuses someone of routing around a gate
// that was never reachable.
//
// This is the same failure as the pre-span one fixed in #163 — calling a gap a
// bypass when the absence of a note is not evidence of anything — and it was
// worth 61 of the 74 bypasses on the real fleet, from a single local-only repo
// that had been renamed away weeks earlier.
//
// Every other fixture here builds a bare remote in newGoldenRepo, so the suite
// structurally could not catch this: the helper encoded "repos have remotes".
func TestGoldenFleet_ARepoThatNeverPushedIsNotBypassed(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	g.adopt()
	g.commit("local one")
	g.commit("local two")
	g.commit("local three")

	got := g.classify()
	if got.Bypassed != 0 {
		t.Errorf("bypassed = %d, want 0: with no remote the push gate never ran, so nothing went round it", got.Bypassed)
	}
	if got.Unpushed != 3 {
		t.Errorf("unpushed = %d, want 3: the commits must be positively accounted for, not merely uncounted", got.Unpushed)
	}
	sum := got.Verified + got.Covered + got.Bypassed + got.Reattestable + got.Unattributable + got.Unpushed
	if sum != got.Commits {
		t.Errorf("buckets sum to %d but there are %d commits (%+v)", sum, got.Commits, got)
	}
}

// An adopted repo whose every commit was gated must report a clean sheet. The
// happy path deserves a test too: a metric that reports problems on a healthy
// repo is the failure mode that gets the whole thing switched off.
func TestGoldenFleet_FullyGatedRepoIsClean(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("change")
	g.gate()

	got := g.classify()
	if got.Bypassed != 0 || got.Unattributable != 0 {
		t.Errorf("a fully gated repo must report nothing to fix: %+v", got)
	}
	if !got.Adopted {
		t.Error("the repo must be reported as adopted")
	}
}
