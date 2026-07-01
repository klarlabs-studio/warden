package service

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// autoApprover approves every gate; the service tests never exercise a real
// human decision.
type autoApprover struct{}

func (autoApprover) Approve(context.Context, application.ApprovalRequest) (application.Decision, error) {
	return application.Decision{Approved: true, Principal: "test"}, nil
}

// initRepo creates a temp git repo with one commit and returns its path,
// skipping the test when git is unavailable.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t.co")
	run("config", "user.name", "t")
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

func TestService_InitWritesConfigHooksAndAdoption(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}

	// Config written with the hook selection.
	cfg, err := svc.Config()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hooks.PreCommit || !cfg.Hooks.PrePush {
		t.Errorf("hooks not recorded in config: %+v", cfg.Hooks)
	}

	// Both shims installed.
	installed, err := svc.InstalledHooks()
	if err != nil {
		t.Fatal(err)
	}
	if !installed[domain.PreCommit] || !installed[domain.PrePush] {
		t.Errorf("hooks not installed: %v", installed)
	}

	// Adoption point recorded at HEAD.
	adoption, err := svc.Repo().ReadAdoption()
	if err != nil || adoption == "" {
		t.Errorf("adoption point not recorded: %q %v", adoption, err)
	}
}

func TestService_InitDoesNotClobberUserConfig(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	// Pre-existing, user-authored config (has a command).
	if err := svc.configs.Save(domain.Config{Commands: map[string]string{"lint": "custom"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Init([]domain.Hook{domain.PrePush}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := svc.Config()
	if cfg.Commands["lint"] != "custom" {
		t.Errorf("init clobbered user config: %+v", cfg.Commands)
	}
	if !cfg.Hooks.PrePush {
		t.Error("init should still sync the hook selection")
	}
}

func TestService_SetHookTogglesShimAndConfig(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	if err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetHook(domain.PreCommit, false); err != nil {
		t.Fatal(err)
	}
	installed, _ := svc.InstalledHooks()
	if installed[domain.PreCommit] {
		t.Error("pre-commit shim should be removed")
	}
	cfg, _ := svc.Config()
	if cfg.Hooks.PreCommit {
		t.Error("config should reflect the disabled hook")
	}
	if _, err := filepath.Abs(dir); err != nil {
		t.Fatal(err)
	}
}

func TestService_ExplainResolvesRule(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	if err := svc.configs.Save(domain.Config{
		Steps: map[string][]domain.StepName{"pre_push": {"review", "lint"}},
		Rules: []domain.Rule{{
			Match: domain.Match{Paths: []string{"security/**"}},
			Then:  domain.Then{Agent: map[domain.StepName]string{"review": "codex"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.Explain(domain.PrePush, "main", []string{"security/token.go"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AgentFor("review") != "codex" {
		t.Errorf("rule not applied via Explain: agent=%q", resolved.AgentFor("review"))
	}
}

func TestService_DoctorRequiresAdoption(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	// No init → no adoption point → doctor should refuse rather than panic.
	if _, err := svc.Doctor(""); err == nil {
		t.Error("doctor without adoption should error")
	}
}
