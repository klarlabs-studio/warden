package config

import (
	"os"
	"path/filepath"
	"slices"
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
symlink_deps: true
writes: [codegen]
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, document, lint]
risk: { diff_lines_high: 400, files_touched_high: 15 }
security_scan: { mode: total, base: origin/main, version_check: false, pin_file: .github/workflows/ci.yml }
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
	if cfg.SymlinkDeps == nil || !*cfg.SymlinkDeps {
		t.Errorf("symlink_deps not parsed: %v", cfg.SymlinkDeps)
	}
	if len(cfg.MaterializeDeps) != 1 || cfg.MaterializeDeps[0] != "build" {
		t.Errorf("materialize_deps not parsed: %+v", cfg.MaterializeDeps)
	}
	if len(cfg.Writes) != 1 || cfg.Writes[0] != "codegen" {
		t.Errorf("writes not parsed: %+v", cfg.Writes)
	}
	if cfg.SecurityScan.Mode != domain.ScanModeTotal || cfg.SecurityScan.Base != "origin/main" {
		t.Errorf("security_scan not parsed: %+v", cfg.SecurityScan)
	}
	if cfg.SecurityScan.VersionCheckEnabled() {
		t.Error("security_scan.version_check: false was not parsed")
	}
	if cfg.SecurityScan.PinFile != ".github/workflows/ci.yml" {
		t.Errorf("security_scan.pin_file = %q", cfg.SecurityScan.PinFile)
	}
}

func TestParse_RejectsBadSecurityScanMode(t *testing.T) {
	// The setting decides how strict the security gate is. A typo must fail the
	// load, not silently resolve to the default.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("security_scan: { mode: strict }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(dir).Load(); err == nil {
		t.Fatal("expected an error on an unknown security_scan.mode")
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

// The churn this guards against is cosmetic in effect but not in consequence:
// it dirties the working tree during unrelated work, and the noisiest part of
// the diff lands on trusted_keys — the one block where a reviewer scanning for
// a changed key should see nothing but real changes (#134).
func TestRepository_SetHooksLeavesFileByteIdenticalOnNoOp(t *testing.T) {
	dir := t.TempDir()
	// Reproduces the shape of warden's own .warden.yaml: a blank separator line
	// and aligned trailing comments, neither of which survives a yaml.Node
	// re-encode.
	const original = `agent: auto
hooks:
  pre_commit: true
  pre_push: true

security_scan:
  pin_file: klarlabs-studio/.github/.github/workflows/go-ci.yml@main

trusted_keys:
  - 139e6eb9e2611c76   # felix — primary dev machine
  - b3746e61c4d49512   # recovery key — seed stored offline
`
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	// Both hooks are ALREADY enabled: this is the no-op that `warden hooks
	// enable` performs when re-pinning after an upgrade.
	if err := repo.SetHooks(domain.HookConfig{PreCommit: true, PrePush: true}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != original {
		t.Errorf("no-op SetHooks rewrote the file.\n--- got ---\n%s\n--- want ---\n%s", out, original)
	}
}

// A real toggle must change the hook line and nothing else — the blank line and
// comment alignment elsewhere are not ours to normalize.
func TestRepository_SetHooksTogglesWithoutReformattingTheRest(t *testing.T) {
	dir := t.TempDir()
	const original = `agent: auto
hooks:
  pre_commit: true
  pre_push: true

trusted_keys:
  - 139e6eb9e2611c76   # felix — primary dev machine
`
	const want = `agent: auto
hooks:
  pre_commit: false
  pre_push: true

trusted_keys:
  - 139e6eb9e2611c76   # felix — primary dev machine
`
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	if err := repo.SetHooks(domain.HookConfig{PreCommit: false, PrePush: true}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Errorf("SetHooks reformatted beyond the toggled key.\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// A trailing comment on the hook line itself documents WHY the hook is set the
// way it is, so it must survive the value changing underneath it.
func TestRepository_SetHooksKeepsCommentOnTheToggledLine(t *testing.T) {
	dir := t.TempDir()
	const original = `hooks:
  pre_commit: true   # fast checks only
  pre_push: false
`
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	if err := repo.SetHooks(domain.HookConfig{PreCommit: false, PrePush: true}); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "# fast checks only") {
		t.Errorf("SetHooks dropped the trailing comment:\n%s", got)
	}
	cfg, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks.PreCommit || !cfg.Hooks.PrePush {
		t.Errorf("hooks not updated: %+v", cfg.Hooks)
	}
}

// The splice deliberately cannot create keys, so a config that predates the
// hooks block must still fall back to the node encoder and come out correct.
// Reformatting is the acceptable cost there; losing the setting is not.
func TestRepository_SetHooksFallsBackWhenKeysAbsent(t *testing.T) {
	dir := t.TempDir()
	const original = `# keep me
agent: auto
`
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	if err := repo.SetHooks(domain.HookConfig{PreCommit: true, PrePush: false}); err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Hooks.PreCommit || cfg.Hooks.PrePush {
		t.Errorf("fallback did not apply the selection: %+v", cfg.Hooks)
	}
	if cfg.Agent != "auto" {
		t.Errorf("fallback disturbed agent: %q", cfg.Agent)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "keep me") {
		t.Errorf("fallback stripped comments:\n%s", out)
	}
}

// Flow style puts both values on one line, where splicing the first would
// invalidate the second's column. The fallback must take over and still be
// correct — a wrong splice would corrupt config rather than merely reflow it.
func TestRepository_SetHooksHandlesFlowStyle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("hooks: {pre_commit: true, pre_push: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(dir)

	if err := repo.SetHooks(domain.HookConfig{PreCommit: false, PrePush: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hooks.PreCommit || !cfg.Hooks.PrePush {
		t.Errorf("flow-style config not updated correctly: %+v", cfg.Hooks)
	}
}

// A file whose last line has no trailing newline must round-trip without one
// being invented — splitLinesKeepEnds is what makes the join lossless.
func TestRepository_SetHooksPreservesMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	const original = "hooks:\n  pre_commit: true\n  pre_push: false"
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
	if want := "hooks:\n  pre_commit: true\n  pre_push: true"; string(out) != want {
		t.Errorf("trailing-newline handling changed the file:\n got %q\nwant %q", out, want)
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

func TestResolveAtRef(t *testing.T) {
	// ResolveAtRef reads committed bytes via a reader keyed on repo-relative path,
	// standing in for `git show <base>:<path>`. It must mirror Load: overlay the
	// extends chain, reject an escape, detect a cycle, cap size.
	reader := func(files map[string]string) FileReaderAtRef {
		return func(rel string) ([]byte, bool, error) {
			data, ok := files[rel]
			return []byte(data), ok, nil
		}
	}

	t.Run("resolves the roster as the union of the extends chain", func(t *testing.T) {
		cfg, err := ResolveAtRef(reader(map[string]string{
			FileName:           "extends: policy/base.yaml\ntrusted_keys:\n  - 1111111111111111\n",
			"policy/base.yaml": "agent: claude\ntrusted_keys:\n  - 2222222222222222\n",
		}))
		if err != nil {
			t.Fatal(err)
		}
		// The roster is the UNION of base + child (a child can't silently drop a
		// base-trusted key), and the base's agent is inherited — same semantics as
		// Load, now resolved from committed bytes at a ref.
		if len(cfg.TrustedKeys) != 2 {
			t.Errorf("roster should union the chain, got %v", cfg.TrustedKeys)
		}
		if !slices.Contains(cfg.TrustedKeys, "1111111111111111") || !slices.Contains(cfg.TrustedKeys, "2222222222222222") {
			t.Errorf("roster union missing a key: %v", cfg.TrustedKeys)
		}
		if cfg.Agent != "claude" {
			t.Errorf("base field not inherited at ref: %q", cfg.Agent)
		}
	})

	t.Run("a missing root config yields a zero config", func(t *testing.T) {
		cfg, err := ResolveAtRef(reader(map[string]string{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.TrustedKeys) != 0 {
			t.Errorf("expected no roster, got %v", cfg.TrustedKeys)
		}
	})

	t.Run("an extends escape is rejected in ref space", func(t *testing.T) {
		for _, esc := range []string{"../../shared.yaml", "/etc/warden/shared.yaml"} {
			_, err := ResolveAtRef(reader(map[string]string{FileName: "extends: " + esc + "\n"}))
			if err == nil {
				t.Errorf("extends %q must be rejected", esc)
			}
		}
	})

	t.Run("an extends cycle errors", func(t *testing.T) {
		_, err := ResolveAtRef(reader(map[string]string{
			FileName: "extends: b.yaml\n",
			"b.yaml": "extends: .warden.yaml\n",
		}))
		if err == nil {
			t.Error("an extends cycle must error")
		}
	})

	t.Run("an oversized config errors", func(t *testing.T) {
		_, err := ResolveAtRef(func(rel string) ([]byte, bool, error) {
			return make([]byte, maxConfigBytes+1), true, nil
		})
		if err == nil {
			t.Error("a config over the byte cap must error")
		}
	})
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
