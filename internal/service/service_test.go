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
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
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

	res, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lang != domain.LangRust {
		t.Fatalf("detected language = %q, want rust", res.Lang)
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
	res, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lang != domain.LangUnknown {
		t.Errorf("no marker should be LangUnknown, got %q", res.Lang)
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
	if _, err := svc.Init([]domain.Hook{domain.PrePush}, false); err != nil {
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
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
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
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
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
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
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
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".warden.yaml")); err != nil {
		t.Fatalf("init should write a starter config when none exists: %v", err)
	}
}

// commitEmpty adds a commit so the adoption point and HEAD can diverge.
//
// --no-verify because these commits land AFTER Init has installed
// warden's own hooks, and the pre-commit shim would try to fetch the
// pinned binary for the test version. What is under test is where the
// adoption point ends up, not whether the gate runs.
func commitEmpty(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--no-verify", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

// #270: a second `init` used to re-record the adoption point at HEAD,
// silently dropping every commit in between out of `warden doctor`'s
// range. Those commits were not reported as unverified — they were
// reported as not existing, and the audit stayed green.
//
// Re-running a setup step is an ordinary thing to do: an
// idempotent-looking line in `make install-hooks`, a README followed
// twice. It must not narrow the audit.
func TestService_InitKeepsAnExistingAdoptionPoint(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Adopted() {
		t.Error("a fresh repository reported a previous adoption point")
	}
	if first.Adoption == "" {
		t.Fatal("no adoption point recorded on first init")
	}

	commitEmpty(t, dir, "commit B")
	commitEmpty(t, dir, "commit C")

	second, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Moved {
		t.Error("a plain second init moved the adoption point")
	}
	if second.Adoption != first.Adoption {
		t.Errorf("adoption point = %s, want the original %s", second.Adoption, first.Adoption)
	}
	if !second.Adopted() {
		t.Error("an already-adopted repository reported no previous point")
	}
	// The record on disk is what doctor reads; asserting only the return
	// value would pass on a function that computed the right answer and
	// wrote the wrong one.
	onDisk, err := svc.Repo().ReadAdoption()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk != first.Adoption {
		t.Errorf("adoption file holds %s, want %s", onDisk, first.Adoption)
	}
}

// Moving the point stays possible, and says what it cost. The count is
// the reason the flag is explicit rather than the default.
func TestService_InitReAdoptMovesAndReportsWhatLeftTheAudit(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	commitEmpty(t, dir, "commit B")
	commitEmpty(t, dir, "commit C")

	moved, err := svc.Init(domain.AllHooks, true)
	if err != nil {
		t.Fatal(err)
	}
	if !moved.Moved {
		t.Fatal("--re-adopt did not move the adoption point")
	}
	if moved.Previous != first.Adoption {
		t.Errorf("Previous = %s, want %s", moved.Previous, first.Adoption)
	}
	if moved.Adoption == first.Adoption {
		t.Error("the adoption point did not advance to HEAD")
	}
	if moved.LeftAudit != 2 {
		t.Errorf("LeftAudit = %d, want 2 — commits B and C just left doctor's range", moved.LeftAudit)
	}
}

// --re-adopt on a repository already at HEAD is not a move, and must not
// be announced as one: a report of commits leaving the audit when none
// did is the same over-claim in the opposite direction.
func TestService_InitReAdoptAtHeadIsNotAMove(t *testing.T) {
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Init(domain.AllHooks, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Moved {
		t.Error("re-adopting at the same HEAD reported a move")
	}
	if again.LeftAudit != 0 {
		t.Errorf("LeftAudit = %d, want 0 — nothing left the range", again.LeftAudit)
	}
}

// emptyRepo creates a repository with no commit — what `git init` leaves, and
// where a new project starts.
func emptyRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "warden-test")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// wardenState lists what an init leaves behind: the config, the hooks, the
// adoption record. "Nothing was changed" is a claim about all three.
func wardenState(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	for _, rel := range []string{
		".warden.yaml",
		filepath.Join(".git", "hooks", "pre-commit"),
		filepath.Join(".git", "hooks", "pre-push"),
		filepath.Join(".git", "warden", "adoption"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			found = append(found, rel)
		}
	}
	return found
}

// `git init && warden init` is the first thing a new project does. It used to
// exit 1 ("read HEAD for adoption point") AFTER installing the hooks and
// writing the config — and the installed pre-commit hook then rejected every
// commit, including the first one the error told the user to go and make.
func TestService_InitInAnEmptyRepositoryAdoptsFromTheFirstCommit(t *testing.T) {
	dir := emptyRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatalf("init in an empty repository: %v", err)
	}
	if res.Adoption != domain.AdoptionRoot {
		t.Errorf("Adoption = %q, want %q", res.Adoption, domain.AdoptionRoot)
	}
	if res.Adopted() {
		t.Error("an empty repository reported a previous adoption point")
	}
	onDisk, err := svc.Repo().ReadAdoption()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk != domain.AdoptionRoot {
		t.Errorf("adoption file holds %q, want %q", onDisk, domain.AdoptionRoot)
	}
	if got := wardenState(t, dir); len(got) != 4 {
		t.Errorf("init left %v, want the config, both hooks and the adoption record", got)
	}

	// The whole point: the first commit is inside the audit. An exclusive
	// adoption point would have had to name a commit before it.
	commitEmpty(t, dir, "first")
	root := gitIn(t, dir, "rev-parse", "HEAD")
	commitEmpty(t, dir, "second")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")

	report, err := svc.Doctor("")
	if err != nil {
		t.Fatal(err)
	}
	if report.Adoption != domain.AdoptionRoot {
		t.Errorf("report.Adoption = %q, want %q", report.Adoption, domain.AdoptionRoot)
	}
	if len(report.Commits) != 2 {
		t.Fatalf("doctor audited %d commits, want 2 — the root commit included", len(report.Commits))
	}
	if oldest := report.Commits[len(report.Commits)-1].SHA; oldest != root {
		t.Errorf("oldest audited commit = %s, want the root %s", oldest, root)
	}
	// Both were pushed with --no-verify: neither may read as anything else.
	if unverified := report.Counts().Unverified; unverified != 2 {
		t.Errorf("Unverified = %d, want 2", unverified)
	}

	// A second init keeps the point, like any other adoption.
	again, err := svc.Init(domain.AllHooks, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Moved || again.Adoption != domain.AdoptionRoot {
		t.Errorf("second init: Moved=%v Adoption=%q, want the root point kept", again.Moved, again.Adoption)
	}

	// Moving off it costs every commit so far, and says so.
	moved, err := svc.Init(domain.AllHooks, true)
	if err != nil {
		t.Fatal(err)
	}
	if !moved.Moved || moved.LeftAudit != 2 {
		t.Errorf("re-adopt from the root: Moved=%v LeftAudit=%d, want a move that drops 2 commits", moved.Moved, moved.LeftAudit)
	}
}

// Init either happens or it does not. Each of these refusals used to leave
// part of a gate behind.
func TestService_ARefusedInitChangesNothing(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		wantErr string
	}{
		{
			name: "a pre-push hook that is not warden's",
			prepare: func(t *testing.T, dir string) {
				hook := filepath.Join(dir, ".git", "hooks", "pre-push")
				if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(hook, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "not warden-managed",
		},
		{
			// Unborn HEAD, but not an empty repository: adopting at the root
			// would pull every other branch's history into the audit.
			name: "an orphan branch in a repository with history",
			prepare: func(t *testing.T, dir string) {
				gitIn(t, dir, "switch", "-q", "--orphan", "fresh")
			},
			wantErr: "nothing was changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := initRepo(t)
			tt.prepare(t, dir)
			before := wardenState(t, dir)

			svc, err := New(dir, "test", autoApprover{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.Init(domain.AllHooks, false)
			if err == nil {
				t.Fatal("init succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			after := wardenState(t, dir)
			if strings.Join(after, ",") != strings.Join(before, ",") {
				t.Errorf("a refused init changed the repository: before %v, after %v", before, after)
			}
		})
	}
}

// Before the first commit there is nothing to audit, and that is the answer —
// not git's "unknown revision or path not in the working tree".
func TestService_DoctorOnABranchWithNoCommits(t *testing.T) {
	dir := emptyRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Init(domain.AllHooks, false); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Doctor("")
	if err == nil {
		t.Fatal("doctor on a branch with no commits returned a report")
	}
	if !strings.Contains(err.Error(), "no commits yet") {
		t.Errorf("error = %q, want it to say the branch has no commits yet", err)
	}
}
