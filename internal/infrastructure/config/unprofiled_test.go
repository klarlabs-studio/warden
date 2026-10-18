package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/policy"
)

// unprofiledChildYAML and unprofiledBaseYAML are a realistic extends chain with
// no `profile:` key. They pin what a repo that never opts into a profile loads
// to, so adding the profile layer cannot change it.
const unprofiledBaseYAML = `
hooks: { pre_commit: true, pre_push: true }
agent: claude
execution: { mode: host }
commands: { lint: "golangci-lint run ./...", test: "go test ./..." }
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, lint]
timeouts: { review: "0" }
signing: { signer: ssh }
trusted_keys: [0123456789abcdef]
`

const unprofiledChildYAML = `
extends: policy/base.yaml
execution: { network: bridge }
writes: [codegen]
rules:
  - match: { paths: ["docs/**"] }
    then: { steps: { pre_push: { skip: [test] } }, auto_fix: { review: 2 } }
`

// unprofiledDigest is DigestJSON of the effective config above, recorded on the
// commit before profiles existed. The configuration digest is part of signed
// execution contracts, so a repo without a profile must keep producing it
// byte for byte.
const unprofiledDigest = "sha256:c4d8335860c87f2886cccb9e152248e3ea2315a441663b91c2762be30d3f9e27"

func writeUnprofiledChain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "policy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy", "base.yaml"), []byte(unprofiledBaseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(unprofiledChildYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_UnprofiledConfigIsUnchanged(t *testing.T) {
	dir := writeUnprofiledChain(t)
	cfg, err := NewRepository(dir).Load()
	if err != nil {
		t.Fatalf("an unprofiled config must load as before: %v", err)
	}
	got, err := domain.DigestJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != unprofiledDigest {
		t.Errorf("configuration digest of an unprofiled repo changed:\n got  %s\n want %s", got, unprofiledDigest)
	}
	res := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "main"})
	if res.ConfigurationDigest != unprofiledDigest {
		t.Errorf("resolved configuration digest changed: %s", res.ConfigurationDigest)
	}
	// Without a profile the top-level agent stays inert, exactly as before:
	// wiring it everywhere would start model calls on every push.
	if len(res.Agents) != 0 {
		t.Errorf("agent: must stay inert without a profile, got %v", res.Agents)
	}
	if cfg.Signing.Required || cfg.Timeouts["review"] != "0" || cfg.Execution.Mode != "host" {
		t.Errorf("no profile defaults may leak into an unprofiled config: %+v", cfg)
	}
}

func TestSave_UnprofiledConfigWritesNoProfileKey(t *testing.T) {
	dir := t.TempDir()
	if err := NewRepository(dir).Save(domain.Config{Hooks: domain.HookConfig{PrePush: true}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// An older warden rejects unknown keys, so a saved config that names
	// `profile:` (even empty) would break every hook on a machine not yet
	// upgraded.
	if strings.Contains(string(data), "profile") {
		t.Errorf("an unprofiled save must not write a profile key:\n%s", data)
	}
}
