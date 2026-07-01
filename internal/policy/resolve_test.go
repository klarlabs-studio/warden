package policy

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// specConfig is the example .warden.yaml from spec §5.1.
const specConfig = `
agent: auto
hooks:
  pre_commit: true
  pre_push: true
commands:
  lint: "golangci-lint run ./..."
  test: "go test -race ./..."
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, document, lint]
risk:
  diff_lines_high: 400
  files_touched_high: 15
rules:
  - match:
      branch: main
    then:
      auto_fix: { review: 0, test: 1 }
      require_approval: true
  - match:
      paths: ["security/**", "auth/**"]
    then:
      steps: { pre_push: { insert_after: lint, add: [security-scan] } }
      agent: { review: codex }
  - match:
      risk: high
    then:
      require_approval: true
      agent: { review: claude }
  - match:
      paths: ["docs/**"]
    then:
      steps: { pre_push: { skip: [test] } }
`

func mustParse(t *testing.T) domain.Config {
	t.Helper()
	cfg, err := Parse([]byte(specConfig))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

func TestResolve_BaselineNoRules(t *testing.T) {
	cfg := mustParse(t)
	got := Resolve(cfg, Input{Hook: domain.PrePush, Branch: "feature/x", Risk: domain.RiskLow})

	want := []domain.StepName{"intent", "rebase", "review", "test", "document", "lint"}
	assertSteps(t, got.Steps, want)
	if got.RequireApproval {
		t.Error("no rule matched; require_approval should be false")
	}
	if len(got.MatchedRules) != 0 {
		t.Errorf("expected no matched rules, got %v", got.MatchedRules)
	}
}

func TestResolve_MainBranchRequiresApprovalAndAutoFix(t *testing.T) {
	cfg := mustParse(t)
	got := Resolve(cfg, Input{Hook: domain.PrePush, Branch: "main", Risk: domain.RiskLow})

	if !got.RequireApproval {
		t.Error("main branch rule sets require_approval: true")
	}
	if b := got.AutoFixBudget("test"); b != 1 {
		t.Errorf("test auto_fix budget = %d, want 1", b)
	}
	if b := got.AutoFixBudget("review"); b != 0 {
		t.Errorf("review auto_fix budget = %d, want 0", b)
	}
}

func TestResolve_SecurityPathsInsertStepAndAgent(t *testing.T) {
	cfg := mustParse(t)
	got := Resolve(cfg, Input{
		Hook:   domain.PrePush,
		Branch: "feature/login",
		Paths:  []string{"security/token.go"},
		Risk:   domain.RiskLow,
	})

	want := []domain.StepName{"intent", "rebase", "review", "test", "document", "lint", "security-scan"}
	assertSteps(t, got.Steps, want)
	if got.AgentFor("review") != "codex" {
		t.Errorf("review agent = %q, want codex", got.AgentFor("review"))
	}
}

func TestResolve_DocsSkipsTest(t *testing.T) {
	cfg := mustParse(t)
	got := Resolve(cfg, Input{
		Hook:  domain.PrePush,
		Paths: []string{"docs/guide.md"},
		Risk:  domain.RiskLow,
	})
	for _, s := range got.Steps {
		if s == "test" {
			t.Fatalf("docs rule should skip test; steps=%v", got.Steps)
		}
	}
}

func TestResolve_HighRiskOverridesReviewAgentBySpecificity(t *testing.T) {
	cfg := mustParse(t)
	// Both the security-paths rule (review: codex) and high-risk rule
	// (review: claude) match. Equal specificity (1 condition each) → later
	// declaration wins. high-risk is declared after security-paths, so claude.
	got := Resolve(cfg, Input{
		Hook:   domain.PrePush,
		Branch: "feature/x",
		Paths:  []string{"security/token.go"},
		Risk:   domain.RiskHigh,
	})
	if got.AgentFor("review") != "claude" {
		t.Errorf("review agent = %q, want claude (later rule, equal specificity)", got.AgentFor("review"))
	}
	if !got.RequireApproval {
		t.Error("high-risk rule requires approval")
	}
}

func TestResolve_PreCommitDefaultSubset(t *testing.T) {
	cfg := mustParse(t)
	got := Resolve(cfg, Input{Hook: domain.PreCommit, Branch: "main", Risk: domain.RiskLow})
	assertSteps(t, got.Steps, []domain.StepName{"lint"})
}

func TestResolve_SkipWinsOverAdd(t *testing.T) {
	cfg := domain.Config{
		Steps: map[string][]domain.StepName{"pre_push": {"lint", "test"}},
		Rules: []domain.Rule{
			{Match: domain.Match{Branch: "main"}, Then: domain.Then{
				Steps: map[string]domain.StepEdit{"pre_push": {Add: []domain.StepName{"extra"}}},
			}},
			{Match: domain.Match{Risk: domain.RiskHigh}, Then: domain.Then{
				Steps: map[string]domain.StepEdit{"pre_push": {Skip: []domain.StepName{"extra"}}},
			}},
		},
	}
	got := Resolve(cfg, Input{Hook: domain.PrePush, Branch: "main", Risk: domain.RiskHigh})
	for _, s := range got.Steps {
		if s == "extra" {
			t.Fatalf("skip should beat add; steps=%v", got.Steps)
		}
	}
}

func TestResolve_UnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte("bogus_field: true\n"))
	if err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func assertSteps(t *testing.T, got, want []domain.StepName) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v, want %v", got, want)
		}
	}
}
