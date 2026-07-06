package service

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepoSvc builds a temp repo + service with a local signer (from
// WARDEN_CONFIG_DIR), the setup every reattest subtest needs.
func newRepoSvc(t *testing.T) (string, *Service) {
	t.Helper()
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := svc.SigningKey(); pub == "" {
		t.Fatal("expected a signer from WARDEN_CONFIG_DIR")
	}
	return dir, svc
}

func TestService_Reattest(t *testing.T) {
	// Empty commits share their parent's tree, so consecutive commits are
	// tree-identical — exactly the squash-merge shape (new id, same content).

	t.Run("carries a validated tree-identical note onto an un-noted commit", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		a := commit(t, dir, svc, "A") // the validated source
		b := commit(t, dir, svc, "B") // squash-like: same tree, no note

		pub, priv, _ := ed25519.GenerateKey(nil)
		if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
			t.Fatal(err)
		}

		res, err := svc.Reattest(b, false)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Wrote || res.Source != a {
			t.Fatalf("expected b re-attested from a, got %+v", res)
		}
		rec, err := svc.Repo().ReadNote(b)
		if err != nil || rec == nil {
			t.Fatalf("no note on b: %v", err)
		}
		if !rec.Attests(b) {
			t.Error("re-attestation must attest the target commit")
		}
		if rec.ReattestedFrom != a {
			t.Errorf("ReattestedFrom = %q, want %s", rec.ReattestedFrom, a)
		}
		myPub, _ := svc.SigningKey()
		if rec.PublicKey != myPub || !rec.VerifySignature() {
			t.Error("re-attestation must be re-signed by the local key and verify")
		}
	})

	t.Run("already-noted commit is left alone", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		a := commit(t, dir, svc, "A")
		pub, priv, _ := ed25519.GenerateKey(nil)
		if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Reattest(a, false)
		if err != nil {
			t.Fatal(err)
		}
		if !res.AlreadyHad || res.Wrote {
			t.Errorf("a already has a note; expected AlreadyHad, got %+v", res)
		}
	})

	t.Run("fail safe: no tree-identical validated source → nothing written", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		a := commit(t, dir, svc, "A")
		pub, priv, _ := ed25519.GenerateKey(nil)
		if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
			t.Fatal(err)
		}
		// A commit with a DIFFERENT tree (a real file) has no validated twin.
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		add := exec.Command("git", "add", "f.txt")
		add.Dir = dir
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
		c := commit(t, dir, svc, "C (changes the tree)")

		res, err := svc.Reattest(c, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Wrote || res.Source != "" {
			t.Errorf("must not re-attest without a tree-identical source: %+v", res)
		}
		if rec, _ := svc.Repo().ReadNote(c); rec != nil {
			t.Error("no note must be written when there is no valid source")
		}
	})

	t.Run("fail safe: an UNSIGNED source is not laundered into a trusted note", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		x := commit(t, dir, svc, "X")
		y := commit(t, dir, svc, "Y") // tree-identical to x
		// x's note attests but is unsigned — a forgeable shape; must be ignored.
		if err := svc.Repo().WriteNote(x, attestRecord(x, "rX")); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Reattest(y, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Wrote || res.Source != "" {
			t.Errorf("an unsigned source must not be re-attested from: %+v", res)
		}
	})
}
