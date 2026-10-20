package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfigs(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The anchor is the line that sets the command in force: a child's command
// overrides its base's, so the child's line wins, and a command only the base
// declares is anchored in the base file.
func TestCommandAnchorsFollowTheExtendsChain(t *testing.T) {
	root := writeConfigs(t, map[string]string{
		FileName: "# repo policy\nextends: policy/base.yaml\ncommands:\n  test: go test ./...\n",
		"policy/base.yaml": "extends: org.yaml\n" +
			"commands:\n" +
			"  lint: golangci-lint run\n" +
			"  test: go test -short ./...\n",
		"policy/org.yaml": "commands: { format: 'gofmt -l .', lint: 'old' }\n",
	})
	got, err := NewRepository(root).CommandAnchors()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Anchor{
		"test":   {File: ".warden.yaml", Line: 4},
		"lint":   {File: "policy/base.yaml", Line: 3},
		"format": {File: "policy/org.yaml", Line: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anchors %+v, want %+v", got, want)
	}
	// The chain must agree with what Load resolves: every anchored command
	// is one the effective configuration runs.
	cfg, err := NewRepository(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["test"] != "go test ./..." || cfg.Commands["lint"] != "golangci-lint run" {
		t.Fatalf("effective commands %v", cfg.Commands)
	}
}

func TestCommandAnchorsWithoutCommands(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no config":           {},
		"empty config":        {FileName: ""},
		"no commands":         {FileName: "notify: false\n"},
		"missing extends":     {FileName: "extends: gone.yaml\n"},
		"scalar commands key": {FileName: "commands: nope\n"},
	} {
		got, err := NewRepository(writeConfigs(t, files)).CommandAnchors()
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestCommandAnchorsRefuseWhatLoadRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"escape": {map[string]string{FileName: "extends: ../outside.yaml\n"}, "escapes repo root"},
		"cycle":  {map[string]string{FileName: "extends: b.yaml\n", "b.yaml": "extends: .warden.yaml\n"}, "cycle"},
		"broken": {map[string]string{FileName: "commands: [\n"}, "parse .warden.yaml"},
	} {
		_, err := NewRepository(writeConfigs(t, tc.files)).CommandAnchors()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	files := map[string]string{}
	for i := range maxExtends + 2 {
		name := FileName
		if i > 0 {
			name = "c" + string(rune('a'+i)) + ".yaml"
		}
		files[name] = "extends: c" + string(rune('a'+i+1)) + ".yaml\n"
	}
	if _, err := NewRepository(writeConfigs(t, files)).CommandAnchors(); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Errorf("deep chain: %v", err)
	}
}
