package domain

import "testing"

func TestPredictableRequiresExplicitReadOnlyContainerShell(t *testing.T) {
	p := ResolvedPolicy{Hook: PreCommit, Execution: ExecutionConfig{Image: "checks:dev"}, PredictiveCache: []string{"test", "review", "security-scan", "custom"}, Commands: map[string]string{"test": "true", "custom": "true", "review": "true", "security-scan": "true"}}
	if !p.Predictable(StepTest) || !p.Predictable(StepName("custom")) {
		t.Fatal("eligible shell checks refused")
	}
	if p.Predictable(StepReview) || p.Predictable(StepSecurityScan) || p.Predictable(StepLint) {
		t.Fatal("non-opted-in or native step allowed")
	}
	p.Hook = PrePush
	if p.Predictable(StepTest) {
		t.Fatal("push reused staged evidence")
	}
	p.Hook = PreCommit
	p.WriteSteps = map[StepName]bool{StepTest: true}
	if p.Predictable(StepTest) {
		t.Fatal("writer cached")
	}
	p.WriteSteps = nil
	p.AutoFix = map[StepName]int{StepTest: 1}
	if p.Predictable(StepTest) {
		t.Fatal("auto-fixer cached")
	}
	p.AutoFix = nil
	p.Execution = ExecutionConfig{}
	if p.Predictable(StepTest) {
		t.Fatal("host cached")
	}
}

func TestPredictiveConfigOverlayAndValidation(t *testing.T) {
	base := Config{PredictiveCache: []string{"test"}}
	if got := (Config{PredictiveCache: []string{}}).OverlayOnto(base); len(got.PredictiveCache) != 0 {
		t.Fatal("explicit empty list did not disable predictive cache")
	}
	if got := (Config{}).OverlayOnto(base); len(got.PredictiveCache) != 1 {
		t.Fatal("unset list did not inherit")
	}
	if err := (Config{PredictiveCache: []string{"../bad"}}).Validate(); err == nil {
		t.Fatal("invalid predictive step accepted")
	}
}

func TestRunCacheReuseDoesNotClaimFreshExecution(t *testing.T) {
	run := newTestRun()
	reuse := &ExecutionReuse{Source: PredictionPurpose, Key: "observed"}
	if err := run.RecordStep(StepResult{Step: StepTest, Status: StepPass, Reuse: reuse, Contract: &ExecutionContract{Version: 1}, Environment: &ExecutionEnvironment{Runtime: "docker"}}); err != nil {
		t.Fatal(err)
	}
	if len(run.ExecutionContracts()) != 0 || len(run.ExecutionEnvironments()) != 0 {
		t.Fatal("cache reuse claimed fresh execution")
	}
	if run.ExecutionReuses()[StepTest].Key != "observed" {
		t.Fatal("missing reuse evidence")
	}
}
