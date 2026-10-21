package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestConfigValidate_Workspaces(t *testing.T) {
	ws := func(w WorkspacesConfig) *WorkspacesConfig { return &w }
	cases := []struct {
		name    string
		cfg     Config
		wantErr string // "" = valid
	}{
		{name: "unset", cfg: Config{}},
		{name: "full block", cfg: Config{
			Workspaces: ws(WorkspacesConfig{Discover: []string{"js", "go"}, Select: "affected", Global: []string{"tools/build/**"}, Ignore: []string{"docs/**", ".github/**"}, Unowned: "full", Concurrency: 4}),
			PerWorkspace: map[string]PerWorkspaceStep{
				"test":      {Each: "pnpm test --dir {workspace.path} # {workspace.name}", Ecosystems: []string{"js"}},
				"typecheck": {Batch: "turbo run typecheck {workspaces.filter} -- {workspaces.paths} {workspaces.names}"},
			},
		}},
		// One file may set part of the block and leave discover to its base;
		// ValidateEffective checks the merged result.
		{name: "rust", cfg: Config{
			Workspaces:   ws(WorkspacesConfig{Discover: []string{"rust", "js"}}),
			PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "cargo test", Ecosystems: []string{"rust"}}},
		}},
		{name: "select alone", cfg: Config{Workspaces: ws(WorkspacesConfig{Select: "affected"})}},
		{name: "each without placeholders runs in the workspace", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"lint": {Each: "make lint"}}}},
		{name: "shell braces are not placeholders", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"lint": {Each: "awk '{print $1}' ${FILE} {a,b}"}}}},

		{name: "unknown ecosystem", cfg: Config{Workspaces: ws(WorkspacesConfig{Discover: []string{"cargo"}})}, wantErr: `unknown ecosystem "cargo"`},
		{name: "duplicate ecosystem", cfg: Config{Workspaces: ws(WorkspacesConfig{Discover: []string{"go", "go"}})}, wantErr: "listed twice"},
		{name: "bad select", cfg: Config{Workspaces: ws(WorkspacesConfig{Discover: []string{"go"}, Select: "changed"})}, wantErr: "workspaces.select"},
		{name: "bad unowned", cfg: Config{Workspaces: ws(WorkspacesConfig{Discover: []string{"go"}, Unowned: "skip"})}, wantErr: "workspaces.unowned"},
		{name: "negative concurrency", cfg: Config{Workspaces: ws(WorkspacesConfig{Discover: []string{"go"}, Concurrency: -1})}, wantErr: "concurrency"},
		{name: "empty glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Global: []string{""}})}, wantErr: "workspaces.global: empty glob"},
		{name: "absolute glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Ignore: []string{"/docs/**"}})}, wantErr: "workspaces.ignore"},
		{name: "backslash glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Ignore: []string{`docs\**`}})}, wantErr: "separate directories with /"},
		{name: "dotdot glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Global: []string{"../shared/**"}})}, wantErr: `".." segment`},
		{name: "trailing slash glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Ignore: []string{"docs/"}})}, wantErr: "empty"},
		{name: "control character glob", cfg: Config{Workspaces: ws(WorkspacesConfig{Global: []string{"a\tb"}})}, wantErr: "control character"},
		{name: "too many segments", cfg: Config{Workspaces: ws(WorkspacesConfig{Global: []string{strings.Repeat("a/", maxGlobSegments) + "a"}})}, wantErr: "segments"},

		{name: "each and batch", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "a", Batch: "b"}}}, wantErr: "not both"},
		{name: "neither each nor batch", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Ecosystems: []string{"js"}}}}, wantErr: "set each:"},
		{name: "blank each", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "  "}}}, wantErr: "set each:"},
		{name: "bad step name", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"a/b": {Each: "x"}}}, wantErr: "invalid step name"},
		{name: "built-in agent step", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"review": {Each: "x"}}}, wantErr: "only shell checks"},
		{name: "security-scan", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"security-scan": {Each: "nox scan ."}}}, wantErr: "only shell checks"},
		{name: "bad step ecosystem", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x", Ecosystems: []string{"python"}}}}, wantErr: "per_workspace.test.ecosystems"},
		{name: "plural placeholder in each", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x {workspaces.filter}"}}}, wantErr: "belongs in a batch: command"},
		{name: "singular placeholder in batch", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Batch: "x {workspace.path}"}}}, wantErr: "belongs in an each: command"},
		{name: "unknown placeholder", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x {workspace.dir}"}}}, wantErr: "unknown placeholder {workspace.dir}"},
		{name: "placeholder in commands", cfg: Config{Commands: map[string]string{"test": "go test ./{workspace.path}/..."}}, wantErr: "commands.test: {workspace.path} is only replaced"},
		{name: "placeholder in agent_commands", cfg: Config{AgentCommands: map[string]string{"claude": "claude -p {workspaces.names}"}}, wantErr: "agent_commands.claude"},
		{name: "defined in both blocks", cfg: Config{
			Commands:     map[string]string{"test": "go test ./..."},
			PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "go test ./..."}},
		}, wantErr: "both commands: and per_workspace:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigValidateEffective(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "nothing set", cfg: Config{}},
		{name: "discover set", cfg: Config{Workspaces: &WorkspacesConfig{Discover: []string{"js"}}, PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x"}}}},
		{name: "block without discover", cfg: Config{Workspaces: &WorkspacesConfig{Select: "affected"}}, wantErr: "workspaces.discover"},
		{name: "per_workspace without workspaces", cfg: Config{PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x"}}}, wantErr: "need workspaces to run over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateEffective()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestOverlayOnto_WorkspacesFieldLevel(t *testing.T) {
	base := Config{Workspaces: &WorkspacesConfig{
		Discover: []string{"js"}, Global: []string{"tools/**"}, Ignore: []string{"docs/**"}, Unowned: UnownedIgnore, Concurrency: 2,
	}}
	child := Config{Workspaces: &WorkspacesConfig{Select: WorkspaceSelectAffected, Global: []string{"scripts/**", "tools/**"}, Ignore: []string{".github/**"}}}
	got := child.OverlayOnto(base).Workspaces
	want := &WorkspacesConfig{
		Discover: []string{"js"}, Select: WorkspaceSelectAffected,
		// global unions, so a child cannot drop a path its base marked global
		Global:  []string{"tools/**", "scripts/**"},
		Ignore:  []string{".github/**"},
		Unowned: UnownedIgnore, Concurrency: 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workspaces = %+v, want %+v", got, want)
	}
	if base.Workspaces.Select != "" || len(base.Workspaces.Global) != 1 {
		t.Errorf("the merge must not write through to the base: %+v", base.Workspaces)
	}
	got.Discover[0] = "go"
	if base.Workspaces.Discover[0] != "js" {
		t.Error("the merged block must not alias the base's slices")
	}
	if got := (Config{}).OverlayOnto(Config{}).Workspaces; got != nil {
		t.Errorf("neither side sets the block: %+v", got)
	}
}

// A step has one definition, and the nearer file's wins whichever block it is
// in: moving a step from commands: to per_workspace: in a child replaces the
// base's definition rather than leaving both.
func TestOverlayOnto_StepDefinitionsChildWins(t *testing.T) {
	base := Config{
		Commands:     map[string]string{"lint": "make lint", "test": "go test ./..."},
		PerWorkspace: map[string]PerWorkspaceStep{"typecheck": {Batch: "tsc -b {workspaces.paths}"}},
	}
	child := Config{
		Commands:     map[string]string{"typecheck": "tsc -b"},
		PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "go test ./..."}},
	}
	got := child.OverlayOnto(base)
	if want := map[string]string{"lint": "make lint", "typecheck": "tsc -b"}; !reflect.DeepEqual(got.Commands, want) {
		t.Errorf("commands = %v, want %v", got.Commands, want)
	}
	if want := map[string]PerWorkspaceStep{"test": {Each: "go test ./..."}}; !reflect.DeepEqual(got.PerWorkspace, want) {
		t.Errorf("per_workspace = %v, want %v", got.PerWorkspace, want)
	}
	if _, ok := base.Commands["test"]; !ok {
		t.Error("the merge must not delete from the base's map")
	}

	// Per key, child wins, within per_workspace too.
	got = Config{PerWorkspace: map[string]PerWorkspaceStep{"typecheck": {Each: "tsc"}}}.OverlayOnto(base)
	if !reflect.DeepEqual(got.PerWorkspace["typecheck"], PerWorkspaceStep{Each: "tsc"}) {
		t.Errorf("child's per_workspace entry must win: %+v", got.PerWorkspace)
	}
	if got.Commands["test"] != "go test ./..." {
		t.Errorf("an unrelated base command must survive: %v", got.Commands)
	}
}

// The configuration digest is part of signed execution contracts, so a config
// that uses neither block must marshal exactly as it did before they existed.
func TestConfig_NoWorkspacesJSONHasNoWorkspaceKeys(t *testing.T) {
	b, err := json.Marshal(Config{Agent: "claude", Commands: map[string]string{"test": "go test ./..."}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Workspace") {
		t.Errorf("a config without workspaces must not marshal a workspace key: %s", b)
	}
	b, _ = json.Marshal(Config{Workspaces: &WorkspacesConfig{Discover: []string{"js"}}, PerWorkspace: map[string]PerWorkspaceStep{"test": {Each: "x"}}})
	for _, want := range []string{`"Workspaces":{"Discover":["js"]}`, `"PerWorkspace":{"test":{"Each":"x"}}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("a config with workspaces must carry %s in its digest: %s", want, b)
		}
	}
}

func TestPerWorkspaceStep_ModeAndCommand(t *testing.T) {
	if s := (PerWorkspaceStep{Each: "a"}); s.Mode() != "each" || s.Command() != "a" {
		t.Errorf("each: %s %q", s.Mode(), s.Command())
	}
	if s := (PerWorkspaceStep{Batch: "b"}); s.Mode() != "batch" || s.Command() != "b" {
		t.Errorf("batch: %s %q", s.Mode(), s.Command())
	}
}
