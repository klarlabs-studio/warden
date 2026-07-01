package config

import (
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestParse_SpecConfig(t *testing.T) {
	const yaml = `
agent: auto
hooks: { pre_commit: true, pre_push: true }
commands: { lint: "golangci-lint run ./...", test: "go test ./..." }
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
