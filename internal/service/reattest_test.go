package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
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

		// a was validated by THIS machine, so its own note is a trusted source.
		if err := svc.Repo().WriteNote(a, signAs(t, svc, attestRecord(a, "rA"))); err != nil {
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

	t.Run("fail safe: an untrusted self-signed source is not laundered into a trusted note", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		a := commit(t, dir, svc, "A")
		b := commit(t, dir, svc, "B") // tree-identical to a
		// a's note attests AND its signature verifies — but by a key we neither pin
		// nor own. Carrying it over would mint trust from an attacker-pushed note.
		pub, priv, _ := ed25519.GenerateKey(nil)
		if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Reattest(b, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Wrote || res.Source != "" {
			t.Errorf("an untrusted self-signed source must not be re-attested from: %+v", res)
		}
		if rec, _ := svc.Repo().ReadNote(b); rec != nil {
			t.Error("no note must be written when the only tree-identical source is untrusted")
		}
	})

	t.Run("a roster-trusted source is carried over", func(t *testing.T) {
		dir, svc := newRepoSvc(t)
		a := commit(t, dir, svc, "A")
		b := commit(t, dir, svc, "B") // tree-identical to a
		pub, priv, _ := ed25519.GenerateKey(nil)
		fp := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(pub))
		// Pin that key in the working-tree roster: now its notes are a trusted source.
		writeConfig(t, dir, "trusted_keys:\n  - "+fp+"\n")
		if err := svc.Repo().WriteNote(a, sign(t, attestRecord(a, "rA"), pub, priv)); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Reattest(b, false)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Wrote || res.Source != a {
			t.Fatalf("a roster-trusted tree-identical source must be carried over: %+v", res)
		}
	})
}

// writeConfig writes a .warden.yaml into a test repo root.
func writeConfig(t *testing.T, dir, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".warden.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestService_TrustedKeysAt is the core Finding-1 proof: a range gate must read
// its trusted-signer roster from the BASE ref, so a later commit (a PR head)
// cannot widen the roster that gates it.
func TestService_TrustedKeysAt(t *testing.T) {
	dir, svc := newRepoSvc(t)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	commitConfig := func(yaml string) string {
		writeConfig(t, dir, yaml)
		git("add", ".warden.yaml")
		git("commit", "--no-verify", "-m", "set roster")
		sha, err := svc.Repo().HeadSHA()
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}

	// The trusted base pins one signer; a later PR-head-like commit tries to add
	// its own key to the roster.
	base := commitConfig("trusted_keys:\n  - 1111111111111111\n")
	head := commitConfig("trusted_keys:\n  - 1111111111111111\n  - deadbeefdeadbeef\n")

	atBase, err := svc.TrustedKeysAt(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(atBase) != 1 || atBase[0] != "1111111111111111" {
		t.Errorf("roster at base must be the base roster only (a PR can't widen it), got %v", atBase)
	}
	// Sanity: the head genuinely added a key, so the assertion above proves base-ref
	// isolation rather than the key simply being absent everywhere.
	atHead, err := svc.TrustedKeysAt(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(atHead) != 2 {
		t.Errorf("head roster should carry the added key, got %v", atHead)
	}
}
