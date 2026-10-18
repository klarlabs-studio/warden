package cli

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// axi verify adds a profiled note's signed observations and adds no key at all
// for an unprofiled one, so existing axi consumers see the output unchanged.
func TestAddProfileObservations(t *testing.T) {
	out := map[string]any{}
	addProfileObservations(out, &domain.RunRecord{})
	if len(out) != 0 {
		t.Fatalf("an unprofiled note must add nothing, got %v", out)
	}

	ref := domain.ProfileRef{Name: "standard", Version: 1, Digest: "d"}
	isolated, changed := true, false
	addProfileObservations(out, &domain.RunRecord{
		Profile: &ref,
		StepOutcomes: map[domain.StepName]domain.StepObservation{
			domain.StepReview: {Outcome: domain.StepExecuted, Agent: "claude", Isolated: &isolated, TreeUnchanged: &changed},
			domain.StepLint:   {Outcome: domain.StepCached},
		},
		Approval: &domain.ApprovalRef{Required: true, Channel: domain.ApprovalTUI, Principal: "warden-tui"},
	})
	if out["profile"] != "standard@1" || out["profile_digest"] != "d" {
		t.Errorf("profile = %v / %v", out["profile"], out["profile_digest"])
	}
	obs, ok := out["recorded_outcomes"].([]any)
	if !ok || len(obs) != 2 {
		t.Fatalf("recorded_outcomes = %v", out["recorded_outcomes"])
	}
	first, _ := obs[0].(map[string]any)
	second, _ := obs[1].(map[string]any)
	if first["step"] != "lint" || first["outcome"] != "cached" || second["agent"] != "claude" {
		t.Errorf("outcomes not sorted or not projected: %v", obs)
	}
	if second["isolated"] != true || second["tree_unchanged"] != false {
		t.Errorf("boundary observations not projected: %v", second)
	}
	if _, ok := second["env_allowlisted"]; ok {
		t.Errorf("an unobserved boundary must not appear: %v", second)
	}
	if _, ok := first["isolated"]; ok {
		t.Errorf("a step without boundary observations must not carry the keys: %v", first)
	}
	if out["approval_required"] != true || out["approval_channel"] != "tui" || out["approval_principal"] != "warden-tui" {
		t.Errorf("approval = %v %v %v", out["approval_required"], out["approval_channel"], out["approval_principal"])
	}
}
