package importer

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestDetectHuskyPreservesHookAlongsideScripts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"scripts":{"lint":"eslint .","test":"vitest"}}`)
	writeFile(t, root, ".husky/pre-commit", "npm run extra-policy\n")
	writeFile(t, root, ".husky/pre-push", "npm test\n")
	writeFile(t, root, ".husky/commit-msg", "commitlint --edit \"$1\"\n")
	cfg, notes, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "npm run lint" || cfg.Commands[string(stepHuskyPreCommit)] != huskyPreCommitCommand {
		t.Fatalf("hook lost to package scripts: %v", cfg.Commands)
	}
	if !reflect.DeepEqual(cfg.Steps["pre_commit"], []domain.StepName{domain.StepLint, stepHuskyPreCommit}) ||
		!reflect.DeepEqual(cfg.Writes, []string{string(stepHuskyPreCommit)}) {
		t.Fatalf("hook must be a pre-commit writer: %+v", cfg)
	}
	for _, step := range cfg.Steps["pre_push"] {
		if step == stepHuskyPreCommit {
			t.Fatal("pre-commit hook scheduled without staged Git context")
		}
	}
	for _, hook := range []string{"pre-push not imported", "commit-msg not imported"} {
		if !containsSubstr(notes, hook) {
			t.Fatalf("unsupported hook not reported: %v", notes)
		}
	}
}

func TestDetectHuskyOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".husky/pre-commit", "echo checked\n")
	cfg, _, err := Detect(root)
	if err != nil || !reflect.DeepEqual(cfg.Steps["pre_commit"], []domain.StepName{stepHuskyPreCommit}) {
		t.Fatalf("husky-only config: %+v, %v", cfg, err)
	}
}

func TestDetectHuskyUnsupportedAndInvalid(t *testing.T) {
	for _, tc := range []struct{ path, content, note string }{
		{".husky/pre-push", "echo check\n", "pre-push not imported"},
		{".husky/pre-commit", " \n", "empty or unreadable"},
		{".husky", "file", "not a regular directory"},
	} {
		t.Run(tc.path+tc.note, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, tc.path, tc.content)
			cfg, notes, err := Detect(root)
			if err != nil || len(cfg.Commands) != 0 || !containsSubstr(notes, tc.note) {
				t.Fatalf("unexpected import: %v %v %v", cfg.Commands, notes, err)
			}
		})
	}
}

func TestDetectHuskyRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	for _, path := range []string{".husky", ".husky/pre-commit"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(root, path)); err != nil {
				t.Fatal(err)
			}
			cfg, notes, err := Detect(root)
			if err != nil || len(cfg.Commands) != 0 || !containsSubstr(notes, "not a regular") {
				t.Fatalf("symlink imported: %v %v %v", cfg.Commands, notes, err)
			}
		})
	}
}

// Exercise the generated command, including Husky's fail-fast behavior and its
// PATH setup. An exit code alone is insufficient: assert the artifact produced
// by the local binary and that execution stops before a later success.
func TestHuskyCommandExecutesAndFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=warden-test", "-c", "user.email=warden-test", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "root"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	writeFile(t, root, "node_modules/.bin/hook-check", "#!/bin/sh\nprintf checked > observed\n")
	if err := os.Chmod(filepath.Join(root, "node_modules/.bin/hook-check"), 0o755); err != nil {
		t.Fatal(err)
	}
	invoke := func() error {
		cmd := exec.Command("sh", "-c", huskyPreCommitCommand)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "HUSKY=0")
		out, err := cmd.CombinedOutput()
		t.Logf("hook output: %s", out)
		return err
	}
	writeFile(t, root, ".husky/pre-commit", "test \"$HUSKY\" = 1\nhook-check\n")
	if err := invoke(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "observed")); err != nil || string(data) != "checked" {
		t.Fatalf("hook did not produce evidence: %q %v", data, err)
	}
	writeFile(t, root, ".husky/pre-commit", "false\nprintf wrong > observed\n")
	if err := invoke(); err == nil {
		t.Fatal("failed first command was masked by a later success")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "observed")); string(data) != "checked" {
		t.Fatal("hook continued after failure")
	}
	writeFile(t, root, ".husky/pre-commit", ". \"$(dirname \"$0\")/_/husky.sh\"\nprintf wrong > observed\n")
	_, notes, _ := Detect(root)
	if !containsSubstr(notes, "legacy bootstrap") || invoke() == nil {
		t.Fatal("missing legacy helper must be reported and fail execution")
	}
	if err := os.Remove(filepath.Join(root, ".husky/pre-commit")); err != nil {
		t.Fatal(err)
	}
	if invoke() == nil {
		t.Fatal("missing hook passed")
	}
}
