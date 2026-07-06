package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"os/exec"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// commit makes an empty, hook-skipping commit on dir and returns the new HEAD
// SHA via svc, so a test can build a multi-commit range deterministically.
func commit(t *testing.T, dir string, svc *Service, msg string) string {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", msg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	sha, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// attestRecord builds a valid, commit-bound, chain-intact (unsigned) record.
func attestRecord(sha, runID string) domain.RunRecord {
	return domain.RunRecord{
		RunID:             runID,
		CommitSHA:         sha,
		StepsRun:          []domain.StepName{"lint", "test"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}, {Hash: "h1", PreviousHash: "h0"}},
	}
}

// sign signs rec in place with priv and records its public key, producing a
// note whose signature verifies.
func sign(t *testing.T, rec domain.RunRecord, pub ed25519.PublicKey, priv ed25519.PrivateKey) domain.RunRecord {
	t.Helper()
	rec.PublicKey = base64.StdEncoding.EncodeToString(pub)
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	return rec
}

func TestService_VerifyRange(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	c1 := commit(t, dir, svc, "c1")
	c2 := commit(t, dir, svc, "c2")
	c3 := commit(t, dir, svc, "c3")
	writeNote := func(sha string, rec domain.RunRecord) {
		if err := svc.Repo().WriteNote(sha, rec); err != nil {
			t.Fatal(err)
		}
	}

	reasonOf := func(res RangeVerifyResult, sha string) domain.VerifyReason {
		for _, v := range res.Commits {
			if v.SHA == sha {
				return v.Reason
			}
		}
		t.Fatalf("commit %s not in range result %+v", sha, res.Commits)
		return ""
	}

	t.Run("a missing note fails the gate", func(t *testing.T) {
		writeNote(c1, attestRecord(c1, "r1"))
		writeNote(c3, attestRecord(c3, "r3"))
		// c2 has no note.
		res, err := svc.VerifyRange(base, c3, RangeVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("gate must fail when a commit in range has no note")
		}
		if got := reasonOf(res, c2); got != domain.ReasonMissing {
			t.Errorf("c2 reason = %q, want %q", got, domain.ReasonMissing)
		}
		if got := reasonOf(res, c1); got != domain.ReasonOK {
			t.Errorf("c1 should pass, got %q", got)
		}
		// The base itself is excluded from base..head — it must not appear.
		for _, v := range res.Commits {
			if v.SHA == base {
				t.Error("base commit must be excluded from the range")
			}
		}
	})

	t.Run("noting the gap makes the gate pass", func(t *testing.T) {
		writeNote(c2, attestRecord(c2, "r2"))
		res, err := svc.VerifyRange(base, c3, RangeVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !res.OK() {
			t.Errorf("gate should pass once every commit is attested: %+v", res.Failures())
		}
		if len(res.Commits) != 3 {
			t.Errorf("range base..c3 should be 3 commits, got %d", len(res.Commits))
		}
	})

	t.Run("a broken-chain note fails even though a note is present", func(t *testing.T) {
		writeNote(c2, domain.RunRecord{CommitSHA: c2, EvidenceChainRoot: "forged", Evidence: []domain.EvidenceEntry{{Hash: "h0"}}})
		res, err := svc.VerifyRange(base, c3, RangeVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := reasonOf(res, c2); got != domain.ReasonBrokenChain {
			t.Errorf("c2 reason = %q, want %q", got, domain.ReasonBrokenChain)
		}
		writeNote(c2, attestRecord(c2, "r2")) // restore for later subtests
	})

	t.Run("require-signed rejects an attested-but-unsigned note", func(t *testing.T) {
		res, err := svc.VerifyRange(base, c3, RangeVerifyOptions{RequireSigned: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("require-signed must fail on unsigned notes")
		}
		if got := reasonOf(res, c1); got != domain.ReasonUnsigned {
			t.Errorf("c1 reason = %q, want %q", got, domain.ReasonUnsigned)
		}
	})

	t.Run("key pinning trusts the right signer and rejects others", func(t *testing.T) {
		pub, priv, _ := ed25519.GenerateKey(nil)
		fp := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(pub))
		writeNote(c1, sign(t, attestRecord(c1, "r1"), pub, priv))
		writeNote(c2, sign(t, attestRecord(c2, "r2"), pub, priv))
		writeNote(c3, sign(t, attestRecord(c3, "r3"), pub, priv))

		trusted, err := svc.VerifyRange(base, c3, RangeVerifyOptions{TrustedKeys: []string{fp}})
		if err != nil {
			t.Fatal(err)
		}
		if !trusted.OK() {
			t.Errorf("pinning the true signer must pass: %+v", trusted.Failures())
		}

		otherPub, _, _ := ed25519.GenerateKey(nil)
		otherFP := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(otherPub))
		untrusted, err := svc.VerifyRange(base, c3, RangeVerifyOptions{TrustedKeys: []string{otherFP}})
		if err != nil {
			t.Fatal(err)
		}
		if untrusted.OK() {
			t.Fatal("pinning a different key must fail")
		}
		if got := reasonOf(untrusted, c1); got != domain.ReasonUntrusted {
			t.Errorf("c1 reason = %q, want %q", got, domain.ReasonUntrusted)
		}
	})
}

func TestService_VerifyRange_SkipMerges(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// A side branch, then a real (non-fast-forward) merge commit onto the trunk.
	git("checkout", "-b", "side")
	side := commit(t, dir, svc, "side work")
	git("checkout", "-")
	trunk := commit(t, dir, svc, "trunk work")
	git("merge", "--no-ff", "--no-edit", "side")
	mergeSHA, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	// Attest the non-merge commits so only the merge could trip the gate.
	for _, sha := range []string{side, trunk} {
		if err := svc.Repo().WriteNote(sha, attestRecord(sha, "r_"+sha[:6])); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("skip-merges omits the un-noted merge commit", func(t *testing.T) {
		res, err := svc.VerifyRange(base, mergeSHA, RangeVerifyOptions{SkipMerges: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range res.Commits {
			if v.SHA == mergeSHA {
				t.Error("merge commit must be skipped")
			}
		}
		if !res.OK() {
			t.Errorf("gate should pass when only the (skipped) merge lacks a note: %+v", res.Failures())
		}
	})

	t.Run("without skip-merges the un-noted merge fails", func(t *testing.T) {
		res, err := svc.VerifyRange(base, mergeSHA, RangeVerifyOptions{SkipMerges: false})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("gate must flag the un-noted merge when merges are not skipped")
		}
	})
}
