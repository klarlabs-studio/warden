package service

import (
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// ErrExternalAttestation is returned when an external attestation cannot be
// written. Unlike the gate path — where provenance is a best-effort side channel
// because the push already happened (§9) — the note IS the product here, so a
// failure is the command failing.
var ErrExternalAttestation = errors.New("external attestation not written")

// ExternalAttestResult reports what was written.
type ExternalAttestResult struct {
	SHA        string
	RunID      string
	AlreadyHad bool
}

// AttestExternal records that an external run executed the checks for a commit,
// and writes the note (ADR 0003).
//
// warden runs nothing here. The record asserts "the signer vouches that run X
// reported these checks passing for this commit" — weaker than a local
// attestation, and marked as such so `verify` can tell them apart. It exists so
// a post-merge CI job can attest the merged commit without re-running checks the
// same pipeline already ran (#177).
//
// Three refusals, all of them the point rather than defensive noise:
//
//   - no signer: an unsigned external note reads as a bare "checks passed" to a
//     consumer with no pinned key, indistinguishable from a local one;
//   - a reference describing another commit: a run against a different tree
//     proves nothing about this one;
//   - nothing to vouch for: no checks means the note asserts a run happened, not
//     that anything passed.
func (s *Service) AttestExternal(commitish string, ref domain.ExternalRunRef, push bool) (ExternalAttestResult, error) {
	sha, err := s.repo.ResolveSHA(commitish)
	if err != nil {
		return ExternalAttestResult{}, fmt.Errorf("resolve %q: %w", commitish, err)
	}
	// Default the reference to the commit being attested, so a caller that knows
	// only "attest HEAD" cannot accidentally leave it empty — but never override
	// an explicit value, because a mismatch is a real disagreement about what ran
	// and must surface as one.
	if ref.Commit == "" {
		ref.Commit = sha
	}
	if s.signer == nil {
		return ExternalAttestResult{SHA: sha}, fmt.Errorf(
			"%w: no signing key available, and an external attestation must be signed", ErrExternalAttestation)
	}

	// Refuse to overwrite an existing sound attestation. Re-attesting would
	// replace a record of something that happened with another one, and if the
	// commit already carries a LOCAL note, replacing it with a weaker external
	// claim is a downgrade nobody asked for.
	if existing, _ := s.repo.ReadNote(sha); existing != nil && existing.Attests(sha) {
		return ExternalAttestResult{SHA: sha, RunID: existing.RunID, AlreadyHad: true}, nil
	}

	root, entries := domain.ExternalEvidence(ref)
	rec := domain.RunRecord{
		RunID:         "run_ext_" + ref.RunID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		WardenVersion: s.version,
		CommitSHA:     sha,
		// StepsRun mirrors the reported checks so the existing readers — doctor's
		// "N steps", the audit export — say something true rather than nothing.
		StepsRun:          stepNames(ref.Checks),
		EvidenceChainRoot: root,
		Evidence:          entries,
		ExternalRun:       &ref,
	}

	rec.PublicKey = s.signer.PublicKey()
	payload, err := rec.SigningPayload()
	if err != nil {
		return ExternalAttestResult{SHA: sha}, err
	}
	if rec.Signature, err = s.signer.Sign(payload); err != nil {
		return ExternalAttestResult{SHA: sha}, fmt.Errorf("%w: sign: %v", ErrExternalAttestation, err)
	}

	// Validate AFTER signing, because "must be signed" is one of the invariants.
	// Checking the finished record is the only check that means anything.
	if err := rec.ValidateExternal(); err != nil {
		return ExternalAttestResult{SHA: sha}, fmt.Errorf("%w: %v", ErrExternalAttestation, err)
	}
	if err := s.repo.WriteNote(sha, rec); err != nil {
		return ExternalAttestResult{SHA: sha}, fmt.Errorf("%w: %v", ErrExternalAttestation, err)
	}
	if push {
		// Not best-effort here: an attestation that reaches no remote is one
		// nobody else can use, and this command exists to publish it. PushNotes
		// reconciles a losing race itself (#186).
		if err := s.repo.PushNotes(s.remote); err != nil {
			return ExternalAttestResult{SHA: sha, RunID: rec.RunID},
				fmt.Errorf("%w: note written but not published: %v", ErrExternalAttestation, err)
		}
	}
	return ExternalAttestResult{SHA: sha, RunID: rec.RunID}, nil
}

// stepNames projects reported check names onto the record's StepsRun.
func stepNames(checks []string) []domain.StepName {
	out := make([]domain.StepName, 0, len(checks))
	for _, c := range checks {
		out = append(out, domain.StepName(c))
	}
	return out
}
