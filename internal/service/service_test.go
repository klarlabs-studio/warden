package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if _, err := svc.Init(domain.AllHooks); err != nil {
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

func TestService_InitDetectsLanguageAndPrefillsCommands(t *testing.T) {
	dir := initRepo(t)
	// Mark the repo as a Rust project.
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, _ := New(dir, "test", autoApprover{})

	lang, err := svc.Init(domain.AllHooks)
	if err != nil {
		t.Fatal(err)
	}
	if lang != domain.LangRust {
		t.Fatalf("detected language = %q, want rust", lang)
	}
	cfg, _ := svc.Config()
	if cfg.Commands["test"] != "cargo test" {
		t.Errorf("test command not pre-filled for rust: %q", cfg.Commands["test"])
	}
	if cfg.Commands["lint"] == "" {
		t.Error("lint command should be pre-filled for a detected language")
	}
}

func TestService_InitUnknownLanguageLeavesPlaceholders(t *testing.T) {
	dir := initRepo(t) // no marker files
	svc, _ := New(dir, "test", autoApprover{})
	lang, err := svc.Init(domain.AllHooks)
	if err != nil {
		t.Fatal(err)
	}
	if lang != domain.LangUnknown {
		t.Errorf("no marker should be LangUnknown, got %q", lang)
	}
	cfg, _ := svc.Config()
	if _, ok := cfg.Commands["lint"]; !ok {
		t.Error("expected empty lint placeholder for an unknown project")
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
	if _, err := svc.Init([]domain.Hook{domain.PrePush}); err != nil {
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
	if _, err := svc.Init(domain.AllHooks); err != nil {
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

func TestService_StepsListReflectsConfig(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	if err := svc.configs.Save(domain.Config{
		Steps: map[string][]domain.StepName{
			"pre_commit": {"lint"},
			"pre_push":   {"review", "test", "custom-scan"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pre, push, err := svc.StepsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 || pre[0] != domain.StepLint {
		t.Errorf("pre-commit steps = %v", pre)
	}
	if len(push) != 3 || push[2] != "custom-scan" {
		t.Errorf("pre-push steps = %v", push)
	}
}

func TestService_StepsListFallsBackToDefaults(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	pre, push, err := svc.StepsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 || len(push) != 7 {
		t.Errorf("defaults expected, got pre=%v push=%v", pre, push)
	}
}

func TestService_ApplyFixPatchEmptyIsNoOp(t *testing.T) {
	dir := initRepo(t)
	svc, _ := New(dir, "test", autoApprover{})
	if err := svc.ApplyFixPatch(""); err != nil {
		t.Errorf("empty patch should be a no-op, got %v", err)
	}
}

func TestService_DoctorFlagsUnverifiedCommit(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}
	// A commit made after adoption with no note is unverified.
	cmd := exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", "post-adoption")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	// It must also have been PUSHED to be a bypass. A branch with no
	// remote-tracking ref never reached the pre-push gate that writes the note,
	// and warden reports those gaps as unattributable rather than as drift.
	nameCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	nameCmd.Dir = dir
	name, err := nameCmd.Output()
	if err != nil {
		t.Fatalf("resolve branch: %v", err)
	}
	ref := "refs/remotes/origin/" + strings.TrimSpace(string(name))
	up := exec.Command("git", "update-ref", ref, "HEAD")
	up.Dir = dir
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("update-ref %s: %v: %s", ref, err, out)
	}
	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	if unverified := report.Counts().Unverified; unverified != 1 {
		t.Errorf("expected 1 unverified commit, got %d (%d commits)", unverified, len(report.Commits))
	}
}

// `warden init` on an existing config must never rewrite it. Authorship used to
// be inferred from the parsed config — "has rules or commands" — so a policy
// built on BUILT-IN steps looked absent and was overwritten wholesale, resetting
// the trusted-signer roster along with everything else.
func TestService_InitPreservesAConfigWithNoCommands(t *testing.T) {
	dir := initRepo(t)
	// steps + trusted_keys, no commands and no rules: entirely built-in steps,
	// which is a perfectly ordinary policy.
	original := "hooks:\n  pre_commit: true\n  pre_push: true\nsteps:\n  pre_commit: [credentials]\ntrusted_keys:\n  - 139e6eb9e2611c76\n"
	path := filepath.Join(dir, ".warden.yaml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.Config()
	if err != nil {
		t.Fatal(err)
	}
	// The roster is the one that matters: losing it silently drops the repo from
	// trusted-signed to attested depth.
	if len(cfg.TrustedKeys) != 1 || cfg.TrustedKeys[0] != "139e6eb9e2611c76" {
		t.Errorf("trusted_keys = %v, want the committed roster preserved", cfg.TrustedKeys)
	}
	steps := cfg.Steps["pre_commit"]
	if len(steps) != 1 || steps[0] != domain.StepCredentials {
		t.Errorf("steps.pre_commit = %v, want [credentials] preserved", steps)
	}
}

// The starter config is still written when there is genuinely no file.
func TestService_InitWritesStarterWhenNoConfigExists(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".warden.yaml")); err != nil {
		t.Fatalf("init should write a starter config when none exists: %v", err)
	}
}
