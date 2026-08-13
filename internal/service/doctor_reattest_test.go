package service

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// initAdopted builds a repo+signer whose adoption point is the initial commit,
// so every commit a test makes afterwards is inside doctor's audit window.
func initAdopted(t *testing.T) (string, *Service) {
	t.Helper()
	dir, svc := newRepoSvc(t)
	if _, err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}
	return dir, svc
}

// The squash-merge case: doctor must not report a bare UNVERIFIED for a commit
// whose exact content a validated commit already covers. Reporting it as a
// clean hole is what leaves the gap unfixed.
func TestService_DoctorMarksReattestable(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A (the gated PR head)")
	b := commit(t, dir, svc, "B (the squash-merge, same tree)")

	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	var status domain.CommitStatus
	for _, c := range report.Commits {
		if c.SHA == b {
			status = c
		}
	}
	if status.SHA == "" {
		t.Fatalf("b not in the audit window: %+v", report.Commits)
	}
	if !status.Reattestable() {
		t.Fatalf("b should be reattestable from a, got %+v", status)
	}
	if status.ReattestableFrom != a {
		t.Errorf("ReattestableFrom = %q, want %s", status.ReattestableFrom, a)
	}
	if got := report.Reattestable(); len(got) != 1 || got[0].SHA != b {
		t.Errorf("report.Reattestable() = %+v, want just b", got)
	}
}

// Doctor must apply the same trust rule Reattest does — advertising a repair
// that reattest would refuse is worse than saying nothing, and pointing at an
// untrusted note as provenance is exactly the laundering reattest guards
// against.
func TestService_DoctorIgnoresUntrustedSource(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A")
	b := commit(t, dir, svc, "B") // tree-identical to a

	// a's note is validly signed, but by a key nobody trusts.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
		t.Fatal(err)
	}

	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Commits {
		if c.SHA == b && c.Reattestable() {
			t.Fatalf("untrusted note must not be advertised as a source: %+v", c)
		}
	}
	// And reattest agrees — doctor's silence is not a false negative.
	res, err := svc.Reattest(b, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrote {
		t.Error("reattest must also refuse an untrusted source")
	}
}

// A commit that genuinely changed the tree was never gated; there is nothing to
// recover and doctor must keep saying so.
func TestService_DoctorLeavesRealHolesAlone(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A")
	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "f.txt")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	c := commit(t, dir, svc, "C (different tree)")

	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range report.Commits {
		if cs.SHA == c && cs.Reattestable() {
			t.Errorf("a tree-changing commit has no source: %+v", cs)
		}
	}
	if got := report.Reattestable(); len(got) != 0 {
		t.Errorf("nothing should be recoverable, got %+v", got)
	}
}

// ReattestAll is the form that actually gets run: one sweep closes every
// recoverable gap on the branch, and re-running it is a no-op.
// TestService_ReattestPlan is #212 §4's core assertion: omitting --push was
// mistaken for a dry run and is not — it still writes local notes. A real plan
// has to leave the note count untouched, and has to agree with the sweep.
func TestService_ReattestPlan(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A (gated)")
	b := commit(t, dir, svc, "B (squash of A)")

	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	before, err := svc.Repo().NotedCommits()
	if err != nil {
		t.Fatal(err)
	}

	plan, err := svc.ReattestPlan("")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Target != b || plan[0].Source != a {
		t.Fatalf("plan should name B recoverable from A, got %+v", plan)
	}

	after, err := svc.Repo().NotedCommits()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a plan must write nothing: notes went %d -> %d", len(before), len(after))
	}

	// And the plan must match what the sweep then does, or it is a lie about
	// what is about to happen.
	results, err := svc.ReattestAll("", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Target != plan[0].Target || results[0].Source != plan[0].Source {
		t.Fatalf("plan %+v disagrees with sweep %+v", plan, results)
	}
}

// The progress callback is the other half of §4: ~94 commits produced no output
// for ten minutes, which reads as a hang. It must fire once per candidate,
// before the work, with a position a human can watch advance.
func TestService_ReattestAllReportsProgress(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A (gated)")
	commit(t, dir, svc, "B (squash 1)")
	commit(t, dir, svc, "C (squash 2)")
	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	var seen []string
	var totals []int
	if _, err := svc.ReattestAll("", false, func(sha string, n, total int) {
		seen = append(seen, sha)
		totals = append(totals, total)
		if n != len(seen) {
			t.Errorf("position should be 1-based and sequential: got %d on call %d", n, len(seen))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one progress call")
	}
	for _, total := range totals {
		if total != len(seen) {
			t.Errorf("total should be the sweep size %d, got %d", len(seen), total)
		}
	}
}

func TestService_ReattestAll(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A (gated)")
	b := commit(t, dir, svc, "B (squash 1)")
	c := commit(t, dir, svc, "C (squash 2)")

	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ReattestAll("", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected b and c re-attested, got %+v", results)
	}
	for _, sha := range []string{b, c} {
		rec, err := svc.Repo().ReadNote(sha)
		if err != nil || rec == nil {
			t.Fatalf("no note written for %s: %v", sha, err)
		}
		if !rec.Attests(sha) || !rec.VerifySignature() {
			t.Errorf("note for %s must attest it and verify: %+v", sha, rec)
		}
		if rec.ReattestedFrom == "" {
			t.Errorf("note for %s must be marked as a re-attestation", sha)
		}
	}

	// The branch is now clean, so a second sweep writes nothing.
	again, err := svc.ReattestAll("", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep should be a no-op, wrote %+v", again)
	}
	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	if unverified := report.Counts().Unverified; unverified != 0 {
		t.Errorf("branch should be fully verified after the sweep, %d unverified", unverified)
	}
}

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// --push means "make the remote match", not "publish what this call happened to
// write". Sweeping without --push and then re-running with it is the obvious
// two-step workflow; gating the push on this invocation having written
// something leaves those notes local forever while the command reports success.
func TestService_ReattestAllPushesEvenWhenNothingNewIsWritten(t *testing.T) {
	dir, svc := initAdopted(t)

	// A bare repo standing in for origin, so PushNotes has somewhere to go.
	remote := t.TempDir()
	gitIn(t, remote, "init", "--bare", ".")
	gitIn(t, dir, "remote", "add", "origin", remote)

	a := commit(t, dir, svc, "A (gated)")
	commit(t, dir, svc, "B (squash, same tree)")
	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	// First sweep: write the notes but deliberately do not publish them.
	wrote, err := svc.ReattestAll("", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrote) == 0 {
		t.Fatal("expected the first sweep to re-attest B")
	}
	if refs := gitIn(t, remote, "for-each-ref", "--format=%(refname)", "refs/notes"); refs != "" {
		t.Fatalf("a push-less sweep must not publish: remote has %q", refs)
	}

	// Second sweep WITH --push: nothing new to write, but the remote is stale.
	again, err := svc.ReattestAll("", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep should write nothing, got %+v", again)
	}
	local := gitIn(t, dir, "rev-parse", "refs/notes/warden")
	published := gitIn(t, remote, "rev-parse", "refs/notes/warden")
	if published != local {
		t.Errorf("--push must publish notes an earlier run left local: remote=%s local=%s", published, local)
	}
}

// The single-commit form has the same trap: a commit that already carries a
// note returns early, and must still honor --push.
func TestService_ReattestPushesWhenTheNoteAlreadyExists(t *testing.T) {
	dir, svc := initAdopted(t)
	remote := t.TempDir()
	gitIn(t, remote, "init", "--bare", ".")
	gitIn(t, dir, "remote", "add", "origin", remote)

	a := commit(t, dir, svc, "A")
	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Reattest(a, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyHad {
		t.Fatalf("expected AlreadyHad, got %+v", res)
	}
	local := gitIn(t, dir, "rev-parse", "refs/notes/warden")
	published := gitIn(t, remote, "rev-parse", "refs/notes/warden")
	if published != local {
		t.Errorf("--push must publish an already-present note: remote=%s local=%s", published, local)
	}
}

// A sweep with nothing recoverable is success, not an error — it means the
// branch has no gap of this kind.
func TestService_ReattestAllEmptyIsNotAnError(t *testing.T) {
	dir, svc := initAdopted(t)
	commit(t, dir, svc, "A (never gated)")

	results, err := svc.ReattestAll("", false, nil)
	if err != nil {
		t.Fatalf("empty sweep must not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("nothing was recoverable, got %+v", results)
	}
}

// #212 §3: the report blamed lost worktree provenance on notes staying local.
// They do not — the gate pushes them. The real mechanism is that a note SURVIVES
// gc while the commit it annotates does not, so `NotedCommits` still lists a SHA
// whose object is gone and reattest skips it silently. Anchoring is what keeps
// the evidence alive; this test destroys the branch and collects the repo, which
// is precisely what `gh pr merge --delete-branch` sets up.
func TestService_AnchoredNoteSurvivesBranchDeleteAndGC(t *testing.T) {
	dir, svc := initAdopted(t)
	trunk := gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	// A real change, so the squash below produces a commit with a DISTINCT sha
	// and an IDENTICAL tree — the exact shape reattest recovers from.
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "work on a branch that will be deleted")
	src := gitIn(t, dir, "rev-parse", "HEAD")
	if err := svc.Repo().WriteNote(src, signAs(t, svc, attestRecord(src, "rS"))); err != nil {
		t.Fatal(err)
	}
	svc.anchorNotedCommits()

	// Squash it onto the trunk the way the default merge flow does, then delete
	// the branch and collect — the source commit's only other ref is now gone.
	gitIn(t, dir, "checkout", "-q", trunk)
	gitIn(t, dir, "merge", "-q", "--squash", "feature")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "squash")
	target := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "branch", "-qD", "feature")
	gitIn(t, dir, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	gitIn(t, dir, "gc", "--prune=now", "-q")

	if _, err := svc.Repo().TreeSHA(src); err != nil {
		t.Fatalf("the anchor should have kept %s reachable through gc: %v", src, err)
	}
	res, err := svc.Reattest(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrote || res.Source != src {
		t.Fatalf("squash commit should still be recoverable from the anchored source, got %+v", res)
	}
}

// #212 §7: re-attestation used to re-sign with the local key, so the repair
// could only be done by a machine already on the roster. The merge-time CI job
// that is the only place it can happen automatically has an ephemeral key, and
// its notes were correctly rejected by the repo's own trusted_keys.
//
// A re-attestation now carries the original record whole, and a verifier judges
// it on that plus tree equality read from git — so the re-attester needs no
// trust and no key. This test does the repair with NO SIGNER AT ALL and requires
// a roster-enforcing verify to accept the result.
func TestService_KeylessReattestIsTrustedViaCarriedOriginal(t *testing.T) {
	dir, svc := initAdopted(t)
	trunk := gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	// The repo enforces a roster — that is the situation §7 describes, where the
	// runner's ephemeral key is correctly rejected by it. Committed on the trunk,
	// so branching and merging do not disturb it.
	roster := []string{svc.signer.Fingerprint()}
	writeTrustedKeys(t, dir, roster)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "pin trusted_keys")

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "gated work")
	src := gitIn(t, dir, "rev-parse", "HEAD")

	// The original validation, by the trusted developer key.
	orig := signAs(t, svc, attestRecord(src, "rOrig"))
	if err := svc.Repo().WriteNote(src, orig); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "checkout", "-q", trunk)
	gitIn(t, dir, "merge", "-q", "--squash", "feature")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "squash")
	target := gitIn(t, dir, "rev-parse", "HEAD")

	// Now be the CI runner: no signing key whatsoever.
	devSigner := svc.signer
	svc.signer = nil
	res, err := svc.Reattest(target, false)
	if err != nil {
		t.Fatalf("a keyless runner must still be able to re-attest: %v", err)
	}
	if !res.Wrote || res.Source != src {
		t.Fatalf("expected a re-attestation from %s, got %+v", src, res)
	}
	svc.signer = devSigner

	note, err := svc.Repo().ReadNote(target)
	if err != nil || note == nil {
		t.Fatalf("no note written: %v", err)
	}
	if note.Signature != "" {
		t.Errorf("a keyless run should not have signed anything, got %q", note.Signature)
	}
	if note.CarriedOriginal == nil || note.CarriedOriginal.CommitSHA != src {
		t.Fatalf("the original record should have been carried, got %+v", note.CarriedOriginal)
	}

	// The point of all of it: a roster-enforcing verify accepts this.
	vr, err := svc.Verify(target, roster...)
	if err != nil {
		t.Fatal(err)
	}
	if !vr.Validated || !vr.Trusted {
		t.Fatalf("carried original should satisfy the roster: %+v", vr)
	}
	if !vr.CarriedTrust {
		t.Error("trust came from the carried original; the result should say so")
	}
}

// The carried original must not become a way to launder provenance onto content
// that was never validated. Tree equality is the check that makes the
// re-attester's honesty irrelevant, so it has to actually bite.
func TestService_CarriedOriginalRejectedWhenTreesDiffer(t *testing.T) {
	dir, svc := initAdopted(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("validated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "validated content")
	src := gitIn(t, dir, "rev-parse", "HEAD")
	orig := signAs(t, svc, attestRecord(src, "rOrig"))
	if err := svc.Repo().WriteNote(src, orig); err != nil {
		t.Fatal(err)
	}
	roster := []string{svc.signer.Fingerprint()}

	// Different content entirely, with a hand-built note that quotes the genuine
	// signed original and points at it. Everything about the quote is authentic;
	// only the claim that the trees match is false.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("never validated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "unvalidated content")
	target := gitIn(t, dir, "rev-parse", "HEAD")

	forged := orig
	forged.CommitSHA = target
	forged.ReattestedFrom = src
	forged.CarriedOriginal = &orig
	forged.PublicKey, forged.Signature = "", ""
	if err := svc.Repo().WriteNote(target, forged); err != nil {
		t.Fatal(err)
	}

	vr, err := svc.Verify(target, roster...)
	if err != nil {
		t.Fatal(err)
	}
	if vr.Trusted || vr.Validated {
		t.Fatalf("differing trees must not be laundered into trust: %+v", vr)
	}
}

// writeTrustedKeys pins a roster in .warden.yaml, so the repo is in the
// enforcing provenance mode.
func writeTrustedKeys(t *testing.T, dir string, keys []string) {
	t.Helper()
	body := "trusted_keys:\n"
	for _, k := range keys {
		body += "  - " + k + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The carried original is only worth carrying because a TRUSTED key signed it.
// A note quoting a self-signed original must not be trusted just because the
// trees line up — otherwise anyone could attest their own commit and then
// relocate that attestation at will.
func TestService_CarriedOriginalRejectedWhenSignerUntrusted(t *testing.T) {
	dir, svc := initAdopted(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "content")
	src := gitIn(t, dir, "rev-parse", "HEAD")

	// Genuinely signed, by a key the roster does not list.
	orig := signAs(t, svc, attestRecord(src, "rOrig"))
	if err := svc.Repo().WriteNote(src, orig); err != nil {
		t.Fatal(err)
	}

	// An empty commit reproduces the tree exactly, so tree equality HOLDS here
	// and the signer is the only thing standing between this and trust.
	gitIn(t, dir, "commit", "--no-verify", "--allow-empty", "-qm", "same tree, new id")
	target := gitIn(t, dir, "rev-parse", "HEAD")

	carried := orig
	relocated := orig
	relocated.CommitSHA = target
	relocated.ReattestedFrom = src
	relocated.CarriedOriginal = &carried
	relocated.PublicKey, relocated.Signature = "", ""
	if err := svc.Repo().WriteNote(target, relocated); err != nil {
		t.Fatal(err)
	}

	vr, err := svc.Verify(target, "someone-elses-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if vr.Trusted || vr.Validated {
		t.Fatalf("an original signed off-roster must not confer trust: %+v", vr)
	}
}

// A keyless re-attestation must be recognized as already-good on the next
// sweep. Otherwise every run finds it untrusted, rewrites it, and --dry-run
// reports work that never finishes.
func TestService_KeylessReattestIsStableAcrossSweeps(t *testing.T) {
	dir, svc := initAdopted(t)
	trunk := gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	roster := []string{svc.signer.Fingerprint()}
	writeTrustedKeys(t, dir, roster)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "pin trusted_keys")

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "gated work")
	src := gitIn(t, dir, "rev-parse", "HEAD")
	if err := svc.Repo().WriteNote(src, signAs(t, svc, attestRecord(src, "rOrig"))); err != nil {
		t.Fatal(err)
	}

	gitIn(t, dir, "checkout", "-q", trunk)
	gitIn(t, dir, "merge", "-q", "--squash", "feature")
	gitIn(t, dir, "commit", "--no-verify", "-qm", "squash")
	target := gitIn(t, dir, "rev-parse", "HEAD")

	svc.signer = nil // the keyless runner
	if _, err := svc.Reattest(target, false); err != nil {
		t.Fatal(err)
	}
	// Second pass: nothing left to do, and the plan agrees.
	res, err := svc.Reattest(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyHad || res.Wrote {
		t.Fatalf("a keyless re-attestation should hold on the next sweep, got %+v", res)
	}
	plan, err := svc.ReattestPlan("")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan {
		if p.Target == target {
			t.Fatalf("plan should not keep proposing an already-good commit: %+v", plan)
		}
	}
}
