package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestService_Verify(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no note is unverified", func(t *testing.T) {
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated || res.SHA != head {
			t.Errorf("expected unvalidated HEAD, got %+v", res)
		}
	})

	t.Run("intact note validates", func(t *testing.T) {
		rec := domain.RunRecord{
			RunID:             "run_x",
			StepsRun:          []domain.StepName{"lint", "test"},
			EvidenceChainRoot: "h0",
			Evidence: []domain.EvidenceEntry{
				{Hash: "h0"},
				{Hash: "h1", PreviousHash: "h0"},
			},
		}
		if err := svc.Repo().WriteNote(head, rec); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Validated || res.Record == nil || res.Record.RunID != "run_x" {
			t.Errorf("expected validated commit with record, got %+v", res)
		}
	})

	t.Run("tampered note fails", func(t *testing.T) {
		bad := domain.RunRecord{
			EvidenceChainRoot: "forged",
			Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
		}
		if err := svc.Repo().WriteNote(head, bad); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated {
			t.Error("a note whose root does not match its chain must not validate")
		}
	})
}

func TestService_Verify_SignedAndPinned(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	fp := domain.KeyFingerprint(pubB64)

	rec := domain.RunRecord{
		RunID:             "run_signed",
		StepsRun:          []domain.StepName{"lint"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
		PublicKey:         pubB64,
	}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}

	t.Run("unpinned verify reports the signer", func(t *testing.T) {
		res, err := svc.Verify("")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Validated || !res.Signed || !res.SignatureValid || res.Signer != fp {
			t.Errorf("expected validated+signed with signer %s, got %+v", fp, res)
		}
		if res.Trusted {
			t.Error("Trusted must be false when no key was pinned")
		}
	})

	t.Run("pinning the correct fingerprint trusts it", func(t *testing.T) {
		res, err := svc.Verify("", fp)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Validated || !res.Trusted {
			t.Errorf("pinning the signer's fingerprint must validate+trust, got %+v", res)
		}
	})

	t.Run("pinning the full public key trusts it", func(t *testing.T) {
		res, err := svc.Verify("", pubB64)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Trusted {
			t.Error("pinning the full public key must trust it")
		}
	})

	t.Run("pinning a different key rejects", func(t *testing.T) {
		otherPub, _, _ := ed25519.GenerateKey(nil)
		res, err := svc.Verify("", domain.KeyFingerprint(base64.StdEncoding.EncodeToString(otherPub)))
		if err != nil {
			t.Fatal(err)
		}
		if res.Validated || res.Trusted {
			t.Error("a note signed by a different key must not validate under pinning")
		}
		if !res.SignatureValid {
			t.Error("the signature itself is still valid; only trust should fail")
		}
	})
}
