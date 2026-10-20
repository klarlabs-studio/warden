package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/policy"
)

// A repo that sets neither workspaces: nor per_workspace: must keep the
// configuration digest it had before the keys existed: the digest is part of
// signed execution contracts. unprofiledDigest was recorded before both.
func TestLoad_WithoutWorkspacesDigestUnchanged(t *testing.T) {
	cfg, err := NewRepository(writeUnprofiledChain(t)).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := domain.DigestJSON(cfg); got != unprofiledDigest {
		t.Errorf("configuration digest changed:\n got  %s\n want %s", got, unprofiledDigest)
	}
	res := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "main"})
	if res.ConfigurationDigest != unprofiledDigest || res.Workspaces != nil {
		t.Errorf("resolution must not gain workspaces: digest=%s workspaces=%+v", res.ConfigurationDigest, res.Workspaces)
	}
}

func TestSave_WithoutWorkspacesWritesNoWorkspaceKeys(t *testing.T) {
	dir := t.TempDir()
	if err := NewRepository(dir).Save(domain.Config{Commands: map[string]string{"test": "go test ./..."}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// An older warden rejects unknown keys, so writing either (even empty)
	// would break every hook on a machine not yet upgraded.
	if strings.Contains(string(data), "workspace") {
		t.Errorf("a save without workspaces must not write the keys:\n%s", data)
	}
}

const workspacesBaseYAML = `
commands: { lint: "make lint", test: "go test ./..." }
workspaces:
  discover: [js]
  global: ["tools/build/**"]
  ignore: ["docs/**"]
`

const workspacesChildYAML = `
extends: policy/base.yaml
workspaces:
  select: affected
  global: ["scripts/**"]
  concurrency: 4
per_workspace:
  test:
    each: "pnpm test"
    ecosystems: [js]
  typecheck:
    batch: "turbo run typecheck {workspaces.filter}"
`

func writeBaseChild(t *testing.T, base, child string) string {
	t.Helper()
	return writeChain(t, map[string]string{"policy/base.yaml": base, FileName: child})
}

func TestLoad_WorkspacesMergeAcrossExtends(t *testing.T) {
	cfg, err, atRef, refErr := loadBoth(t, writeBaseChild(t, workspacesBaseYAML, workspacesChildYAML))
	if err != nil || refErr != nil {
		t.Fatalf("load: %v / at ref: %v", err, refErr)
	}
	want := &domain.WorkspacesConfig{
		Discover: []string{"js"}, Select: domain.WorkspaceSelectAffected,
		Global: []string{"tools/build/**", "scripts/**"}, Ignore: []string{"docs/**"}, Concurrency: 4,
	}
	if !reflect.DeepEqual(cfg.Workspaces, want) {
		t.Errorf("workspaces = %+v, want %+v", cfg.Workspaces, want)
	}
	// The child moved test from the base's commands: to per_workspace:.
	if _, ok := cfg.Commands["test"]; ok || cfg.Commands["lint"] != "make lint" {
		t.Errorf("commands = %v: the child's per_workspace definition of test must replace the base's", cfg.Commands)
	}
	if len(cfg.PerWorkspace) != 2 || cfg.PerWorkspace["test"].Each != "pnpm test" {
		t.Errorf("per_workspace = %+v", cfg.PerWorkspace)
	}

	// The same chain resolved from git objects agrees.
	if !reflect.DeepEqual(atRef, cfg) {
		t.Errorf("ResolveAtRef and Load disagree:\n%+v\n%+v", atRef, cfg)
	}
}

func TestLoad_WorkspacesRefusals(t *testing.T) {
	cases := []struct {
		name, base, child, wantErr string
	}{
		{
			name:    "per_workspace with no workspaces anywhere in the chain",
			base:    "commands: { lint: make lint }\n",
			child:   "extends: policy/base.yaml\nper_workspace: { test: { each: go test ./... } }\n",
			wantErr: "with its extends chain",
		},
		{
			name:    "workspaces without discover anywhere in the chain",
			base:    "commands: { lint: make lint }\n",
			child:   "extends: policy/base.yaml\nworkspaces: { select: affected }\n",
			wantErr: "workspaces.discover",
		},
		{
			name:    "a base that breaks a rule is named",
			base:    "workspaces: { discover: [js], ignore: [\"/abs/**\"] }\n",
			child:   "extends: policy/base.yaml\n",
			wantErr: "base.yaml",
		},
		{
			name:    "unknown key under workspaces",
			base:    "commands: { lint: make lint }\n",
			child:   "extends: policy/base.yaml\nworkspaces: { discover: [js], sparse: true }\n",
			wantErr: "sparse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRepository(writeBaseChild(t, tc.base, tc.child)).Load()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
