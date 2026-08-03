package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	return g.wardenEnv(nil, args...)
}

// wardenEnv is warden with extra environment, for the cases where the
// ENVIRONMENT is the thing under test.
func (g *goldenRepo) wardenEnv(extra []string, args ...string) (string, int) {
	g.t.Helper()
	cmd := exec.Command(wardenBin, args...)
	cmd.Dir = g.dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+g.t.TempDir())
	cmd.Env = append(cmd.Env, extra...)
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

// revParse resolves a ref to a SHA, for asserting that something did NOT move.
func (g *goldenRepo) revParse(ref string) string {
	g.t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("rev-parse %s: %v: %s", ref, err, out)
	}
	return strings.TrimSpace(string(out))
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
	Defective      int  `json:"defective"`
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

// The number CI actually gates on is `warden doctor`'s exit code, and it was the
// one surface the assertions above did not reach. They read the fleet rollup,
// which excluded span-covered commits correctly; doctor's own tally did not, so
// this exact repo printed "✓ (covered by the gated push …)" for every commit and
// exited 1 anyway. Two classifications of one fact, and the test suite only ever
// asked the one that was right.
func TestGoldenFleet_DoctorExitsZeroOnAGatedMultiCommitPush(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("first of three")
	g.commit("second of three")
	g.commit("third of three")
	g.gate()

	out, code := g.warden("doctor")
	if code != 0 {
		t.Errorf("doctor exited %d on a fully gated three-commit push, want 0\n%s", code, out)
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
	sum := got.Verified + got.Covered + got.Bypassed + got.Reattestable + got.Unattributable + got.Unpushed + got.Defective
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
	sum := got.Verified + got.Covered + got.Bypassed + got.Reattestable + got.Unattributable + got.Unpushed + got.Defective
	if sum != got.Commits {
		t.Errorf("buckets sum to %d but there are %d commits (%+v)", sum, got.Commits, got)
	}
	// Same gap as the multi-commit case: doctor printed "These are not bypasses"
	// and then failed on them anyway, because its tally counted every note-less
	// commit as unverified regardless of whether the gate could ever have run.
	out, code := g.warden("doctor")
	if code != 0 {
		t.Errorf("doctor exited %d on a branch that was never pushed, want 0\n%s", code, out)
	}
}

// The forge-merge gap, and the CI mode that closes it.
//
// warden's gate is client-side pre-push, so a commit the forge creates itself —
// a GitHub squash-merge, a web edit, a merged Dependabot PR — can never carry a
// note. Every one of the 11 remaining "bypasses" on the fleet this was written
// against was exactly that: committed by GitHub, not by a person evading
// anything. `--attest-only` is what a post-merge CI job runs to attest the
// merged commit: gate the tree, write the note, leave the branch alone.
func TestGoldenFleet_AttestOnlyClosesTheForgeMergeGap(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()

	// Simulate what the forge does: a commit lands on the published branch that
	// warden's pre-push gate never saw.
	g.commit("merged by the forge")
	g.git("push", "--no-verify", "origin", "main")

	before := g.classify()
	if before.Bypassed != 1 {
		t.Fatalf("bypassed = %d, want 1 before the CI job runs (%+v)", before.Bypassed, before)
	}

	// The post-merge CI job.
	if out, code := g.warden("run", "pre-push", "--attest-only"); code != 0 && code != 3 {
		t.Fatalf("attest-only: %d %s", code, out)
	}

	after := g.classify()
	if after.Bypassed != 0 {
		t.Errorf("bypassed = %d, want 0 after attesting the merged commit (%+v)", after.Bypassed, after)
	}
	if after.Verified != before.Verified+1 {
		t.Errorf("verified = %d, want %d: the merged commit must now carry a note", after.Verified, before.Verified+1)
	}
}

// A passing --attest-only run must exit 0 and must not claim it pushed.
//
// This is the interface, and it is what actually broke in production. The two
// tests around it assert the EFFECT — the branch does not move, the commit
// stops being a bypass — and both passed while the run exited 3 ("passed;
// warden performed the push") about a commit warden never touched. Exit 3
// failed the CI step, so the note was written and then never published: the
// gate passed and the commit stayed unattested.
//
// Asserting the effect is not enough when a caller keys on the exit code.
func TestGoldenFleet_AttestOnlyExitsZeroAndClaimsNoPush(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("merged by the forge")
	g.git("push", "--no-verify", "origin", "main")

	out, code := g.warden("run", "pre-push", "--attest-only")
	if code != 0 {
		t.Errorf("exit = %d, want 0: --attest-only pushes nothing, so there is no stale push for git to stand down from\n%s", code, out)
	}
	if strings.Contains(out, "pushed") && !strings.Contains(out, "nothing pushed") {
		t.Errorf("output claims a push that did not happen:\n%s", out)
	}
	if !strings.Contains(out, "attested") {
		t.Errorf("a passing attest-only run should say what it DID (attest), got:\n%s", out)
	}
}

// An --attest-only run that cannot write its note must FAIL, not report success.
//
// This is the exact CI condition: `git notes add` writes a commit on
// refs/notes/warden and needs a committer identity, which actions/checkout does
// not configure. warden treated the failure as best-effort — right for the gate
// path, where the push already happened and provenance is a side-channel —
// swallowed it, printed
//
//	gate passed; attested b27a99d471cc
//
// and exited 0. Every step of the workflow went green and nothing was attested.
// A green run that did nothing is worse than a red one: it stops anybody looking.
//
// Under --attest-only the note is the whole product of the run, so there is
// nothing left to succeed at if it cannot be written.
func TestGoldenFleet_AttestOnlyFailsWhenTheNoteCannotBeWritten(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("merged by the forge")
	g.git("push", "--no-verify", "origin", "main")

	// Strip the committer identity, reproducing a bare CI checkout. Unsetting the
	// REPO config is not enough — a developer's global ~/.gitconfig would supply
	// one and the run would quietly succeed, testing nothing. The global and
	// system files have to be neutralized too, which is precisely the state
	// actions/checkout leaves a runner in.
	g.git("config", "--unset", "user.email")
	g.git("config", "--unset", "user.name")
	noIdentity := []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=",
		"GIT_AUTHOR_EMAIL=",
		"GIT_COMMITTER_NAME=",
		"GIT_COMMITTER_EMAIL=",
	}

	out, code := g.wardenEnv(noIdentity, "run", "pre-push", "--attest-only")
	if code == 0 {
		t.Errorf("exit = 0 with no identity to write the note: a run that attested nothing must not report success\n%s", out)
	}
	if strings.Contains(out, "attested") && !strings.Contains(out, "could not") {
		t.Errorf("output claims an attestation that was never written:\n%s", out)
	}
}

// --attest-only must not move the branch. The branch is already published —
// that is what triggered the job — and pushing from CI would race the next human
// push and fail on a stale ref.
func TestGoldenFleet_AttestOnlyLeavesTheBranchAlone(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("merged by the forge")
	g.git("push", "--no-verify", "origin", "main")

	head := g.revParse("HEAD")
	remote := g.revParse("refs/remotes/origin/main")

	if out, code := g.warden("run", "pre-push", "--attest-only"); code != 0 && code != 3 {
		t.Fatalf("attest-only: %d %s", code, out)
	}

	if got := g.revParse("HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s: --attest-only must not rewrite the branch", head, got)
	}
	if got := g.revParse("refs/remotes/origin/main"); got != remote {
		t.Errorf("origin/main moved from %s to %s: --attest-only must not push", remote, got)
	}
}

// The RENDERED summary must account for every commit too, not just the JSON.
//
// TestGoldenFleet_BucketsAccountForEveryCommit reads the JSON report, so a
// bucket that is tallied but never PRINTED passes it — which is exactly what
// happened when Defective was added: the JSON balanced while the human line
// showed 124 of 131 commits. The printed line is what people actually read, so
// it needs its own guard.
func TestGoldenFleet_RenderedSummaryAccountsForEveryCommit(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.commit("gated one")
	g.commit("gated two")
	g.gate()
	g.commit("bypassed")
	g.git("push", "--no-verify", "origin", "main")

	out, _ := g.warden("fleet", "status", g.dir)
	counts := g.classify()

	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "commits since adoption:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no summary line in fleet output:\n%s", out)
	}
	// Sum every "<n> <bucket>" pair in the line, so a bucket added to the report
	// and forgotten in the renderer reads as a shortfall rather than as nothing.
	sum := 0
	pairs := regexp.MustCompile(`(\d+) (verified|covered by a gated push|bypassed|reattestable|unattributable|unpushed|with a defective note)`)
	for _, m := range pairs.FindAllStringSubmatch(line, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparsable count %q in %q", m[1], line)
		}
		sum += n
	}
	if sum != counts.Commits {
		t.Errorf("the printed summary accounts for %d commits but there are %d\n  line: %s\n  json: %+v",
			sum, counts.Commits, line, counts)
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
