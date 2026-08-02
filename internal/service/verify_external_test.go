package service

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// externalRecord is attestRecord plus an external-run reference bound to the
// same commit — what a CI-attested note looks like (ADR 0003).
func externalRecord(sha, runID string) domain.RunRecord {
	rec := attestRecord(sha, runID)
	rec.ExternalRun = &domain.ExternalRunRef{
		Provider:   "github-actions",
		RunID:      "30747937107",
		Repository: "klarlabs-studio/warden",
		Commit:     sha,
		Checks:     []string{"lint", "test"},
	}
	return rec
}

func serviceWithNote(t *testing.T, rec func(sha string) domain.RunRecord) (*Service, string) {
	t.Helper()
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	sha := commit(t, dir, svc, "c1")
	if err := svc.Repo().WriteNote(sha, signAs(t, svc, rec(sha))); err != nil {
		t.Fatal(err)
	}
	return svc, sha
}

// The default REFUSES an external attestation, and this is the security-relevant
// behavior in ADR 0003.
//
// Every consumer of `warden verify` today means "warden ran the checks". Had
// external attestations been accepted by default, every gate already deployed
// would have started accepting a weaker claim the moment it upgraded, without
// anyone choosing it.
func TestVerify_RefusesAnExternalAttestationByDefault(t *testing.T) {
	svc, sha := serviceWithNote(t, func(sha string) domain.RunRecord {
		return externalRecord(sha, "r-ext")
	})

	res, err := svc.Verify(sha)
	if err != nil {
		t.Fatal(err)
	}
	if res.Validated {
		t.Error("plain Verify must not validate an external attestation")
	}
	if !res.External {
		t.Error("the result must report that the note WAS external")
	}
	// The distinction that stops someone hunting for a missing note: this is not
	// "no provenance", it is "provenance of a kind you did not ask for".
	if !res.ExternalRefused {
		t.Error("a sound note refused only for being external must say so")
	}
}

// Opting in accepts it. This is the point of the feature: one CI run, and the
// attestation records what actually ran.
func TestVerify_AllowAcceptsAnExternalAttestation(t *testing.T) {
	svc, sha := serviceWithNote(t, func(sha string) domain.RunRecord {
		return externalRecord(sha, "r-ext")
	})

	res, err := svc.VerifyWithPolicy(sha, ExternalAllow)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Validated {
		t.Errorf("ExternalAllow must accept a sound external attestation: %+v", res)
	}
	if res.ExternalRefused {
		t.Error("nothing was refused")
	}
}

// A local note is unaffected by the policy existing — the whole design rests on
// not changing what an ordinary note means.
func TestVerify_LocalAttestationIsUnaffected(t *testing.T) {
	svc, sha := serviceWithNote(t, func(sha string) domain.RunRecord {
		return attestRecord(sha, "r-local")
	})

	for _, p := range []ExternalPolicy{ExternalReject, ExternalAllow} {
		res, err := svc.VerifyWithPolicy(sha, p)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Validated || res.External {
			t.Errorf("policy %v: local note must validate and not read as external: %+v", p, res)
		}
	}
}

// ExternalRequire is for a branch whose policy is that CI, not a developer's
// machine, is the attester of record. A local note must not satisfy it, or the
// policy is decorative.
func TestVerify_RequireRefusesALocalAttestation(t *testing.T) {
	svc, sha := serviceWithNote(t, func(sha string) domain.RunRecord {
		return attestRecord(sha, "r-local")
	})

	res, err := svc.VerifyWithPolicy(sha, ExternalRequire)
	if err != nil {
		t.Fatal(err)
	}
	if res.Validated {
		t.Error("ExternalRequire must not accept a local attestation")
	}
	if !res.ExternalRefused {
		t.Error("the refusal reason must be reportable")
	}
}

// An external note whose reference describes a DIFFERENT commit must not
// validate even when external attestations are allowed. A run against another
// tree proves nothing about this one.
func TestVerify_ExternalRunBoundToAnotherCommitIsNotValid(t *testing.T) {
	svc, sha := serviceWithNote(t, func(sha string) domain.RunRecord {
		rec := externalRecord(sha, "r-ext")
		rec.ExternalRun.Commit = "0000000000000000000000000000000000000000"
		return rec
	})

	res, err := svc.VerifyWithPolicy(sha, ExternalAllow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Validated {
		t.Error("a run against another commit must not validate this one")
	}
}
