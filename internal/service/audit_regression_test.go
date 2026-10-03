package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestExternalRangePolicy(t *testing.T) {
	dir, s := newRepoSvc(t)
	base, _ := s.repo.HeadSHA()
	sha := commit(t, dir, s, "external")
	if _, err := s.AttestExternal(sha, extRef("lint"), false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []ExternalPolicy{ExternalReject, ExternalAllow, ExternalRequire} {
		single, err := s.VerifyWithPolicy(sha, p, s.signer.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		ranged, err := s.VerifyRange(base, sha, RangeVerifyOptions{ExternalPolicy: p, TrustedKeys: []string{s.signer.Fingerprint()}, RequireSigned: true})
		if err != nil {
			t.Fatal(err)
		}
		if ranged.OK() != single.Validated || ranged.OK() != (p != ExternalReject) {
			t.Fatalf("policy %d single=%t range=%t", p, single.Validated, ranged.OK())
		}
	}
}
func TestCarriedClaimCannotPromoteExternalOrChangeEvidence(t *testing.T) {
	for _, external := range []bool{false, true} {
		dir, s := newRepoSvc(t)
		src := commit(t, dir, s, "source")
		dst := commit(t, dir, s, "same tree")
		orig := signAs(t, s, attestRecord(src, "original"))
		if external {
			ref := extRef("lint")
			ref.Commit = src
			orig.ExternalRun = &ref
			orig = signAs(t, s, orig)
		}
		wrapper := orig
		wrapper.CommitSHA = dst
		wrapper.ReattestedFrom = src
		wrapper.CarriedOriginal = &orig
		wrapper.PublicKey = ""
		wrapper.Signature = ""
		if external {
			wrapper.ExternalRun = nil
		} else {
			wrapper.StepsRun = []domain.StepName{"invented-check"}
		}
		if err := s.repo.WriteNote(dst, wrapper); err != nil {
			t.Fatal(err)
		}
		got, err := s.Verify(dst, s.signer.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		if got.Validated {
			t.Fatalf("changed claim accepted: %+v", got)
		}
	}
}
func rejectingNotesRemote(t *testing.T, dir string) string {
	t.Helper()
	remote := t.TempDir()
	for _, item := range []struct {
		dir  string
		args []string
	}{{remote, []string{"init", "--bare"}}, {dir, []string{"remote", "add", "origin", remote}}} {
		cmd := exec.Command("git", item.args...)
		cmd.Dir = item.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return remote
}
func assertNoRemoteNote(t *testing.T, remote, sha string) {
	t.Helper()
	cmd := exec.Command("git", "-C", remote, "notes", "--ref=warden", "show", sha)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unexpected remote note: %s", out)
	}
}
func TestReattestPublicationFailureIsReturned(t *testing.T) {
	dir, s := newRepoSvc(t)
	remote := rejectingNotesRemote(t, dir)
	src := commit(t, dir, s, "source")
	dst := commit(t, dir, s, "target")
	if err := s.repo.WriteNote(src, signAs(t, s, attestRecord(src, "original"))); err != nil {
		t.Fatal(err)
	}
	result, err := s.Reattest(dst, true)
	if err == nil || !result.Wrote {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if note, err := s.repo.ReadNote(dst); err != nil || note == nil {
		t.Fatalf("local note missing: %v", err)
	}
	assertNoRemoteNote(t, remote, dst)
	if _, err := s.Reattest(dst, true); err == nil {
		t.Fatal("already-noted retry hid publication failure")
	}
}
func TestExternalPublicationRetryHonorsPush(t *testing.T) {
	dir, s := newRepoSvc(t)
	remote := rejectingNotesRemote(t, dir)
	sha := commit(t, dir, s, "external")
	if _, err := s.AttestExternal(sha, extRef("lint"), false); err != nil {
		t.Fatal(err)
	}
	if result, err := s.AttestExternal(sha, extRef("lint"), true); err == nil || !result.AlreadyHad {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	assertNoRemoteNote(t, remote, sha)
	if err := os.Remove(filepath.Join(remote, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttestExternal(sha, extRef("lint"), true); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", remote, "notes", "--ref=warden", "show", sha)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) == 0 {
		t.Fatalf("retry did not publish: %v %s", err, out)
	}
}
