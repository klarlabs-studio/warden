package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// policy_explain returns the ResolvedPolicy as JSON, so the profile reaches an
// agent through it with no tool change. Without a profile the output must not
// change at all.
func TestHandlePolicyExplain_CarriesTheProfile(t *testing.T) {
	prof := &domain.ResolvedProfile{Name: "strict@1", Level: "strict", Digest: "sha256:abc",
		Floors: []domain.ProfileFloorStatus{{ID: domain.FloorNetworkNone, Description: "execution.network is none", Satisfied: true}}}
	got, err := handlePolicyExplain(&fakeFacade{policy: domain.ResolvedPolicy{Hook: domain.PrePush, Profile: prof}}, PolicyExplainInput{Hook: "pre-push"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Profile":{`, `"Name":"strict@1"`, `"Digest":"sha256:abc"`, `"ID":"network-none"`, `"Satisfied":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy_explain JSON missing %s: %s", want, b)
		}
	}

	got, err = handlePolicyExplain(&fakeFacade{policy: domain.ResolvedPolicy{Hook: domain.PrePush}}, PolicyExplainInput{Hook: "pre-push"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(got)
	if strings.Contains(string(b), "Profile") {
		t.Errorf("an unprofiled policy must not gain a Profile key: %s", b)
	}
}

// policy_explain carries what per-workspace steps do in the hook, so an agent
// reads the same account `warden policy explain` prints.
func TestHandlePolicyExplain_CarriesWhatRunsPerWorkspace(t *testing.T) {
	notice := domain.WorkspaceExecutionNotice(domain.PrePush, domain.WorkspaceSelectAffected)
	ws := &domain.ResolvedWorkspaces{Discover: []string{"js"}, Select: domain.WorkspaceSelectAffected, Active: true, Notice: notice,
		Steps: map[domain.StepName]domain.PerWorkspaceStep{"test": {Each: "pnpm test"}}}
	got, err := handlePolicyExplain(&fakeFacade{policy: domain.ResolvedPolicy{Hook: domain.PrePush, Workspaces: ws}}, PolicyExplainInput{Hook: "pre-push"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	for _, want := range []string{`"Active":true`, `"Each":"pnpm test"`, "trusted full-scope note"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy_explain JSON missing %s: %s", want, b)
		}
	}
}
