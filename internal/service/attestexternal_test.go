package service

import (
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func extRef(checks ...string) domain.ExternalRunRef {
	return domain.ExternalRunRef{
		Provider:   "github-actions",
		RunID:      "30747937107",
		Repository: "klarlabs-studio/warden",
		Checks:     checks,
	}
}

func repoForAttest(t *testing.T) (*Service, string) {
	t.Helper()
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, commit(t, dir, svc, "merged by the forge")
}

// The happy path: a commit the forge created gets an attestation naming the run
// that actually did the work — which is the whole point of #177.
func TestAttestExternal_WritesASignedBoundRecord(t *testing.T) {
	svc, sha := repoForAttest(t)

	res, err := svc.AttestExternal(sha, extRef("lint", "test"), false)
	if err != nil {
		t.Fatalf("AttestExternal: %v", err)
	}
	if res.SHA != sha {
		t.Errorf("attested %s, want %s", res.SHA, sha)
	}

	rec, err := svc.Repo().ReadNote(sha)
	if err != nil || rec == nil {
		t.Fatalf("note not written: %v", err)
	}
	if !rec.IsExternal() {
		t.Error("the record must be marked external, or a consumer cannot tell it apart")
	}
	if !rec.Attests(sha) {
		t.Error("the record must be self-consistent and bound: the evidence chain has to verify")
	}
	if !rec.Signed() || !rec.VerifySignature() {
		t.Error("an external attestation must carry a signature that verifies")
	}
	if err := rec.ValidateExternal(); err != nil {
		t.Errorf("the written record must satisfy its own invariants: %v", err)
	}
}

// …and it is accepted only when the caller opts in, end to end.
func TestAttestExternal_IsRefusedByDefaultAndAcceptedOnOptIn(t *testing.T) {
	svc, sha := repoForAttest(t)
	if _, err := svc.AttestExternal(sha, extRef("lint"), false); err != nil {
		t.Fatal(err)
	}

	if res, _ := svc.Verify(sha); res.Validated {
		t.Error("plain Verify must not accept it")
	}
	res, err := svc.VerifyWithPolicy(sha, ExternalAllow)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Validated {
		t.Errorf("ExternalAllow must accept it: %+v", res)
	}
}

// A reference describing a DIFFERENT commit must be refused rather than
// silently rewritten to match. The mismatch is a real disagreement about what
// ran, and papering over it is how "CI passed" becomes "some CI passed".
func TestAttestExternal_RefusesAReferenceForAnotherCommit(t *testing.T) {
	svc, sha := repoForAttest(t)
	ref := extRef("lint")
	ref.Commit = "0000000000000000000000000000000000000000"

	_, err := svc.AttestExternal(sha, ref, false)
	if !errors.Is(err, ErrExternalAttestation) {
		t.Fatalf("err = %v, want ErrExternalAttestation", err)
	}
	if rec, _ := svc.Repo().ReadNote(sha); rec != nil {
		t.Error("nothing must be written when the reference does not match")
	}
}

// No checks means the note would assert that a run happened, not that anything
// passed — an attestation vouching for nothing.
func TestAttestExternal_RefusesWithNoChecks(t *testing.T) {
	svc, sha := repoForAttest(t)

	_, err := svc.AttestExternal(sha, extRef(), false)
	if !errors.Is(err, ErrExternalAttestation) {
		t.Fatalf("err = %v, want ErrExternalAttestation", err)
	}
	if !strings.Contains(err.Error(), "checks") {
		t.Errorf("the message must name what is missing: %v", err)
	}
}

// An existing sound attestation is never replaced. Overwriting a LOCAL note —
// warden actually ran those checks — with a weaker external claim is a downgrade
// nobody asked for, and it would happen on every re-run of a CI job.
func TestAttestExternal_DoesNotOverwriteAnExistingAttestation(t *testing.T) {
	svc, sha := repoForAttest(t)
	local := signAs(t, svc, attestRecord(sha, "run-local"))
	if err := svc.Repo().WriteNote(sha, local); err != nil {
		t.Fatal(err)
	}

	res, err := svc.AttestExternal(sha, extRef("lint"), false)
	if err != nil {
		t.Fatalf("re-attesting an already-attested commit is not an error: %v", err)
	}
	if !res.AlreadyHad {
		t.Error("the result must say nothing was written")
	}

	rec, _ := svc.Repo().ReadNote(sha)
	if rec == nil || rec.IsExternal() {
		t.Error("the existing LOCAL attestation must survive untouched")
	}
	if rec.RunID != "run-local" {
		t.Errorf("run id = %q, want the original local run", rec.RunID)
	}
}

// The evidence chain must verify, and must be per-check rather than a single
// opaque entry — a reader walking the chain should see what was claimed.
func TestExternalEvidence_ChainsPerCheckAndVerifies(t *testing.T) {
	ref := extRef("lint", "test", "security-scan")
	ref.Commit = "abc"
	root, entries := domain.ExternalEvidence(ref)

	if len(entries) != 3 {
		t.Fatalf("got %d entries, want one per check", len(entries))
	}
	rec := domain.RunRecord{CommitSHA: "abc", EvidenceChainRoot: root, Evidence: entries}
	if !rec.VerifyChain() {
		t.Error("the chain must verify")
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Kind, "external.") {
			t.Errorf("entry %q must be marked external, so a chain walker can see it was reported, not observed", e.Kind)
		}
	}
	// The commit is inside each hash, so entries cannot be lifted onto a record
	// attesting a different commit.
	other := ref
	other.Commit = "def"
	_, otherEntries := domain.ExternalEvidence(other)
	if entries[0].Hash == otherEntries[0].Hash {
		t.Error("evidence for a different commit must hash differently")
	}
}
