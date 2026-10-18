package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// profiledRecord is attestRecord with every profile-gated field set.
func profiledRecord(t *testing.T, sha, runID string) domain.RunRecord {
	t.Helper()
	p, err := domain.ParseComplianceProfile("standard")
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.NewProfileRef(p)
	rec := attestRecord(sha, runID)
	rec.Profile = &ref
	rec.StepOutcomes = map[domain.StepName]domain.StepObservation{
		domain.StepLint:   {Outcome: domain.StepExecuted},
		domain.StepReview: {Outcome: domain.StepSkipped},
	}
	rec.Approval = &domain.ApprovalRef{Required: true, Channel: domain.ApprovalTTY, Principal: "warden-cli"}
	return rec
}

func profileFieldsJSON(t *testing.T, rec *domain.RunRecord) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		P any `json:"profile"`
		S any `json:"step_outcomes"`
		A any `json:"approval"`
	}{rec.Profile, rec.StepOutcomes, rec.Approval})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A re-attestation relocates a validation; it must not edit what that
// validation observed. The profile, step outcomes and approval are carried
// byte for byte, and a re-attester that strips or rewrites any of them no
// longer matches the carried original's payload, so the re-attestation stops
// holding.
func TestService_ReattestCarriesProfileObservationsByteForByte(t *testing.T) {
	dir, svc := newRepoSvc(t)
	a := commit(t, dir, svc, "A")
	b := commit(t, dir, svc, "B") // same tree as a
	src := signAs(t, svc, profiledRecord(t, a, "rA"))
	if err := svc.Repo().WriteNote(a, src); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Reattest(b, false)
	if err != nil || !res.Wrote {
		t.Fatalf("reattest: %+v %v", res, err)
	}
	rec, err := svc.Repo().ReadNote(b)
	if err != nil || rec == nil {
		t.Fatalf("no note on b: %v", err)
	}
	if got, want := profileFieldsJSON(t, rec), profileFieldsJSON(t, &src); !bytes.Equal(got, want) {
		t.Errorf("re-attestation altered the observations:\n got %s\nwant %s", got, want)
	}
	if got, want := profileFieldsJSON(t, rec.CarriedOriginal), profileFieldsJSON(t, &src); !bytes.Equal(got, want) {
		t.Errorf("carried original lost the observations:\n got %s\nwant %s", got, want)
	}
	trust := svc.reattestTrustSet()
	if !svc.carriedOriginalHolds(b, rec, trust) {
		t.Fatal("an unaltered re-attestation of a profiled note must hold")
	}

	for name, edit := range map[string]func(*domain.RunRecord){
		"profile stripped": func(r *domain.RunRecord) { r.Profile = nil },
		"outcome upgraded": func(r *domain.RunRecord) {
			r.StepOutcomes = map[domain.StepName]domain.StepObservation{domain.StepLint: {Outcome: domain.StepExecuted}, domain.StepReview: {Outcome: domain.StepExecuted}}
		},
		"approval relabelled":   func(r *domain.RunRecord) { r.Approval = &domain.ApprovalRef{Channel: domain.ApprovalNone} },
		"observations stripped": func(r *domain.RunRecord) { r.Profile, r.StepOutcomes, r.Approval = nil, nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			edited := *rec
			edit(&edited)
			if svc.carriedOriginalHolds(b, &edited, trust) {
				t.Errorf("a re-attestation with the %s must not hold", name)
			}
		})
	}
}
