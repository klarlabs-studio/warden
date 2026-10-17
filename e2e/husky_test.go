package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_HuskyImportRunsStagedHook(t *testing.T) {
	g := newGoldenRepoNoRemote(t)
	if err := os.Mkdir(filepath.Join(g.dir, ".husky"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g.dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The script independently checks both views of the staged snapshot. Its
	// output artifact proves that the original hook actually executed. The
	// artifact is written outside the disposable tree solely for this fixture.
	artifact := filepath.Join(t.TempDir(), "observed")
	write(".husky/pre-commit", "test \"$(cat input)\" = staged\ntest \"$(git show :input)\" = staged\nprintf observed > \"$WARDEN_HUSKY_TEST_ARTIFACT\"\n")
	write("input", "initial")
	write("evidence", "initial")
	g.git("add", ".husky/pre-commit", "input", "evidence")
	g.git("commit", "--no-verify", "-m", "hook fixture")
	if out, code := g.warden("import", "--write"); code != 0 || !strings.Contains(out, "husky-pre-commit") {
		t.Fatalf("import: %d %s", code, out)
	}
	if data, err := os.ReadFile(filepath.Join(g.dir, ".warden.yaml")); err != nil || !strings.Contains(string(data), "writes:") {
		t.Fatalf("imported writer config missing: %s %v", data, err)
	}
	write("input", "staged")
	g.git("add", "input", ".warden.yaml")
	write("input", "unstaged")
	if out, code := g.wardenEnv([]string{"HUSKY=0", "WARDEN_HUSKY_TEST_ARTIFACT=" + artifact}, "run", "pre-commit"); code != 0 {
		t.Fatalf("staged hook: %d %s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(g.dir, "input")); string(data) != "unstaged" {
		t.Fatal("hook disturbed unstaged input")
	}
	if data, _ := os.ReadFile(artifact); string(data) != "observed" {
		t.Fatalf("hook did not produce evidence: %q", data)
	}
	write(".husky/pre-commit", "false\nprintf masked > evidence\n")
	g.git("add", ".husky/pre-commit")
	if out, code := g.warden("run", "pre-commit"); code == 0 {
		t.Fatalf("failing hook accepted: %s", out)
	}
	if data, _ := os.ReadFile(filepath.Join(g.dir, "evidence")); string(data) == "masked" {
		t.Fatal("failure was masked by later successful command")
	}
	for _, hook := range []string{"printf changed > evidence\n", "printf changed > evidence\ngit add evidence\n"} {
		write(".husky/pre-commit", hook)
		g.git("add", ".husky/pre-commit")
		if out, code := g.warden("run", "pre-commit"); code == 0 || !strings.Contains(out, "changed tracked files/index") {
			t.Fatalf("mutating hook must fail: %d %s", code, out)
		}
		if data, _ := os.ReadFile(filepath.Join(g.dir, "evidence")); string(data) != "initial" {
			t.Fatalf("mutation escaped worktree: %q", data)
		}
	}
}
