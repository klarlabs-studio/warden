package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a throwaway repo so newService (which opens a git repo) works.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t.co"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestCmdImport_DryRun(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("lint:\n\tx\ntest:\n\ty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	var out, errb bytes.Buffer
	if code := cmdImport(nil, &out, &errb); code != 0 {
		t.Fatalf("cmdImport: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "make lint") || !strings.Contains(out.String(), "would write") {
		t.Errorf("dry-run output missing detected command or preview: %q", out.String())
	}
	// Dry run must not create the file.
	if _, err := os.Stat(filepath.Join(dir, ".warden.yaml")); !os.IsNotExist(err) {
		t.Error("dry-run should not write .warden.yaml")
	}
}

func TestCmdImport_Write(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"eslint .","test":"jest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	var out, errb bytes.Buffer
	if code := cmdImport([]string{"--write"}, &out, &errb); code != 0 {
		t.Fatalf("cmdImport --write: code=%d err=%q", code, errb.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".warden.yaml"))
	if err != nil {
		t.Fatalf("expected .warden.yaml written: %v", err)
	}
	if !strings.Contains(string(data), "npm run lint") {
		t.Errorf(".warden.yaml missing imported command: %s", data)
	}
}

func TestCmdImport_Empty(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	chdir(t, dir)

	var out, errb bytes.Buffer
	if code := cmdImport(nil, &out, &errb); code != 0 {
		t.Fatalf("cmdImport: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing imported") {
		t.Errorf("expected 'nothing imported' note, got %q", out.String())
	}
}
