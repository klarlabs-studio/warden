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
func TestService_ReattestAll(t *testing.T) {
	dir, svc := initAdopted(t)
	a := commit(t, dir, svc, "A (gated)")
	b := commit(t, dir, svc, "B (squash 1)")
	c := commit(t, dir, svc, "C (squash 2)")

	if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ReattestAll("", false)
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
	again, err := svc.ReattestAll("", false)
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
	if _, _, unverified := report.Counts(); unverified != 0 {
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
	wrote, err := svc.ReattestAll("", false)
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
	again, err := svc.ReattestAll("", true)
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

	results, err := svc.ReattestAll("", false)
	if err != nil {
		t.Fatalf("empty sweep must not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("nothing was recoverable, got %+v", results)
	}
}
