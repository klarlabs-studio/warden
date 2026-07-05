package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestParse_SpecConfig(t *testing.T) {
	const yaml = `
agent: auto
hooks: { pre_commit: true, pre_push: true }
commands: { lint: "golangci-lint run ./...", test: "go test ./..." }
materialize_deps: [build]
writes: [codegen]
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, document, lint]
risk: { diff_lines_high: 400, files_touched_high: 15 }
rules:
  - match: { paths: ["security/**"] }
    then: { require_approval: true, agent: { review: codex } }
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "auto" || !cfg.Hooks.PrePush {
		t.Errorf("unexpected header fields: %+v", cfg.Hooks)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Then.Agent["review"] != "codex" {
		t.Errorf("rule not parsed: %+v", cfg.Rules)
	}
	if cfg.Risk.DiffLinesHigh != 400 {
		t.Errorf("risk threshold = %d, want 400", cfg.Risk.DiffLinesHigh)
	}
	if len(cfg.MaterializeDeps) != 1 || cfg.MaterializeDeps[0] != "build" {
		t.Errorf("materialize_deps not parsed: %+v", cfg.MaterializeDeps)
	}
	if len(cfg.Writes) != 1 || cfg.Writes[0] != "codegen" {
		t.Errorf("writes not parsed: %+v", cfg.Writes)
	}
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	if _, err := Parse([]byte("bogus_field: true\n")); err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestRepository_LoadMissingIsZero(t *testing.T) {
	cfg, err := NewRepository(t.TempDir()).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 0 || cfg.Agent != "" {
		t.Errorf("missing config should be zero, got %+v", cfg)
	}
}

func TestRepository_SetHooksPreservesComments(t *testing.T) {
	dir := t.TempDir()
	const original = `# Warden policy — keep this comment
agent: auto
hooks:
  pre_commit: true
  pre_push: false
commands:
  lint: "golangci-lint run ./..." # quality gate
`
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	if err := repo.SetHooks(domain.HookConfig{PreCommit: true, PrePush: true}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// Comments survive.
	if !strings.Contains(got, "keep this comment") || !strings.Contains(got, "quality gate") {
		t.Errorf("SetHooks stripped comments:\n%s", got)
	}
	// The toggled value took effect and the rest is intact.
	cfg, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hooks.PrePush || !cfg.Hooks.PreCommit {
		t.Errorf("hooks not updated: %+v", cfg.Hooks)
	}
	if cfg.Commands["lint"] != "golangci-lint run ./..." {
		t.Errorf("SetHooks disturbed commands: %q", cfg.Commands["lint"])
	}
}

func TestRepository_SetHooksCreatesWhenAbsent(t *testing.T) {
	repo := NewRepository(t.TempDir())
	if err := repo.SetHooks(domain.HookConfig{PrePush: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hooks.PrePush {
		t.Errorf("SetHooks on missing file should create it, got %+v", cfg.Hooks)
	}
}

func TestRepository_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := NewRepository(dir)
	want := domain.Config{Agent: "codex", Hooks: domain.HookConfig{PrePush: true}}
	if err := repo.Save(want); err != nil {
		t.Fatal(err)
	}
	if _, err := filepath.Glob(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "codex" || !got.Hooks.PrePush {
		t.Errorf("round trip = %+v, want agent=codex prePush=true", got)
	}
}

func TestRepository_LoadResolvesExtends(t *testing.T) {
	repoDir := t.TempDir()
	// Base config committed inside the repo (a versioned org-policy file); the
	// containment rule requires an extends target to stay within the repo root.
	baseDir := filepath.Join(repoDir, "policy")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(baseDir, "base.yaml")
	if err := os.WriteFile(base, []byte("agent: claude\ncommands:\n  lint: \"golangci-lint run\"\n  test: \"go test ./...\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := "extends: policy/base.yaml\ncommands:\n  test: \"go test -race ./...\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, FileName), []byte(child), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := NewRepository(repoDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "claude" {
		t.Errorf("agent not inherited from base: %q", cfg.Agent)
	}
	if cfg.Commands["lint"] != "golangci-lint run" {
		t.Errorf("base command not inherited: %q", cfg.Commands["lint"])
	}
	if cfg.Commands["test"] != "go test -race ./..." {
		t.Errorf("child override lost: %q", cfg.Commands["test"])
	}
}

func TestRepository_ExtendsEscapeRejected(t *testing.T) {
	// A repo config must not inherit from a file outside the repo root, whether
	// via a ".." escape or an absolute path — such a file is un-versioned and
	// could smuggle commands: that later run via `sh -c`.
	cases := map[string]string{
		"parent-escape": "extends: ../../shared.yaml\n",
		"absolute":      "extends: /etc/warden/shared.yaml\n",
	}
	for name, child := range cases {
		t.Run(name, func(t *testing.T) {
			repoDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(repoDir, FileName), []byte(child), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := NewRepository(repoDir).Load()
			if err == nil {
				t.Fatal("expected extends escaping the repo root to error")
			}
			if !strings.Contains(err.Error(), "escapes repo root") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepository_InvalidStepNameRejected(t *testing.T) {
	// A custom step name containing a path separator would be treated by
	// exec.LookPath("warden-step-"+name) as a relative path; reject it at load.
	repoDir := t.TempDir()
	yaml := "steps:\n  pre_push: [lint, \"x/evil\"]\n"
	if err := os.WriteFile(filepath.Join(repoDir, FileName), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewRepository(repoDir).Load()
	if err == nil {
		t.Fatal("expected an invalid step name to error")
	}
	if !strings.Contains(err.Error(), "invalid step name") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRepository_LoadRejectsOversizedConfig(t *testing.T) {
	repoDir := t.TempDir()
	big := strings.Repeat("# padding comment line\n", (maxConfigBytes/23)+2)
	if err := os.WriteFile(filepath.Join(repoDir, FileName), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewRepository(repoDir).Load()
	if err == nil {
		t.Fatal("expected an oversized config to error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRepository_ExtendsCycleErrors(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, FileName)
	b := filepath.Join(dir, "b.yaml")
	if err := os.WriteFile(a, []byte("extends: b.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("extends: .warden.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(dir).Load(); err == nil {
		t.Error("an extends cycle must error")
	}
}
