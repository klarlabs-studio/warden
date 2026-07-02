package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// writeFile writes content to root/rel, creating parent dirs as needed.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect_Empty(t *testing.T) {
	cfg, notes, err := Detect(t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(cfg.Commands) != 0 {
		t.Errorf("expected zero config, got commands %v", cfg.Commands)
	}
	if len(notes) != 1 {
		t.Errorf("expected one 'nothing imported' note, got %v", notes)
	}
}

func TestDetect_Makefile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Makefile", `
.PHONY: lint test audit
VAR := ignored
lint: deps
	golangci-lint run
vet:
	go vet ./...
test:
	go test ./...
audit:
	govulncheck ./...
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	// lint target preferred over vet.
	if got := cfg.Commands["lint"]; got != "make lint" {
		t.Errorf("lint = %q, want %q", got, "make lint")
	}
	if got := cfg.Commands["test"]; got != "make test" {
		t.Errorf("test = %q, want %q", got, "make test")
	}
	if got := cfg.Commands["security-scan"]; got != "make audit" {
		t.Errorf("security-scan = %q, want %q", got, "make audit")
	}
	assertSecurityStep(t, cfg, true)
}

func TestDetect_MakefilePrefersLintOverFmtVet(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Makefile", "fmt:\n\tgofmt -w .\nvet:\n\tgo vet ./...\nlint:\n\tgolangci-lint run\n")
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Commands["lint"]; got != "make lint" {
		t.Errorf("lint = %q, want make lint (lint preferred over fmt/vet)", got)
	}
}

func TestDetect_PackageJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{
	  "scripts": {
	    "lint": "eslint .",
	    "test": "vitest run",
	    "audit": "npm audit --production"
	  }
	}`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "npm run lint" {
		t.Errorf("lint = %q", cfg.Commands["lint"])
	}
	if cfg.Commands["test"] != "npm test" {
		t.Errorf("test = %q", cfg.Commands["test"])
	}
	if cfg.Commands["security-scan"] != "npm audit" {
		t.Errorf("security-scan = %q", cfg.Commands["security-scan"])
	}
}

func TestDetect_Lefthook(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "lefthook.yml", `
pre-commit:
  commands:
    lint:
      run: golangci-lint run {staged_files}
pre-push:
  commands:
    test:
      run: go test ./...
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "golangci-lint run {staged_files}" {
		t.Errorf("lint = %q", cfg.Commands["lint"])
	}
	if cfg.Commands["test"] != "go test ./..." {
		t.Errorf("test = %q", cfg.Commands["test"])
	}
}

func TestDetect_Workflows(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".github/workflows/ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Lint
        run: golangci-lint run ./...
      - name: Test
        run: go test ./...
`)
	cfg, notes, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "golangci-lint run ./..." {
		t.Errorf("lint = %q", cfg.Commands["lint"])
	}
	if cfg.Commands["test"] != "go test ./..." {
		t.Errorf("test = %q", cfg.Commands["test"])
	}
	if !containsSubstr(notes, "heuristic") {
		t.Errorf("expected a best-effort/heuristic note, got %v", notes)
	}
}

// TestDetect_Priority verifies Makefile > package.json > lefthook > workflows
// when several sources define the same command.
func TestDetect_Priority(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Makefile", "lint:\n\tmake-lint\n")
	writeFile(t, root, "package.json", `{"scripts":{"lint":"eslint .","test":"jest"}}`)
	writeFile(t, root, "lefthook.yml", "pre-commit:\n  commands:\n    lint:\n      run: lefthook-lint\n    test:\n      run: lefthook-test\n")
	writeFile(t, root, ".github/workflows/ci.yml", "jobs:\n  b:\n    steps:\n      - run: eslint --lint\n      - run: go test ./...\n")

	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	// Makefile wins lint.
	if cfg.Commands["lint"] != "make lint" {
		t.Errorf("lint = %q, want make lint (Makefile priority)", cfg.Commands["lint"])
	}
	// package.json wins test (Makefile has no test target).
	if cfg.Commands["test"] != "npm test" {
		t.Errorf("test = %q, want npm test (package.json priority over lefthook/workflows)", cfg.Commands["test"])
	}
}

// TestDetect_ConfigShape checks the non-command fields are always set as spec'd.
func TestDetect_ConfigShape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Makefile", "lint:\n\tx\ntest:\n\ty\n")
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "auto" {
		t.Errorf("Agent = %q, want auto", cfg.Agent)
	}
	if !cfg.Hooks.PreCommit || cfg.Hooks.PrePush {
		t.Errorf("Hooks = %+v, want pre_commit:true pre_push:false", cfg.Hooks)
	}
	if cfg.Risk != domain.RiskConfig(domain.DefaultRiskThresholds()) {
		t.Errorf("Risk = %+v, want defaults", cfg.Risk)
	}
	pre := cfg.Steps[domain.PreCommit.ConfigKey()]
	if len(pre) != 1 || pre[0] != domain.StepLint {
		t.Errorf("pre_commit steps = %v, want [lint]", pre)
	}
	// No security source, so pre_push is [rebase, lint, test].
	push := cfg.Steps[domain.PrePush.ConfigKey()]
	want := []domain.StepName{domain.StepRebase, domain.StepLint, domain.StepTest}
	if !equalSteps(push, want) {
		t.Errorf("pre_push steps = %v, want %v", push, want)
	}
	assertSecurityStep(t, cfg, false)
}

func assertSecurityStep(t *testing.T, cfg domain.Config, want bool) {
	t.Helper()
	got := false
	for _, s := range cfg.Steps[domain.PrePush.ConfigKey()] {
		if s == "security-scan" {
			got = true
		}
	}
	if got != want {
		t.Errorf("security-scan in pre_push = %v, want %v (steps %v)", got, want, cfg.Steps[domain.PrePush.ConfigKey()])
	}
}

func equalSteps(a, b []domain.StepName) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSubstr(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
