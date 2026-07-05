package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCreateWorktreeFromHead_TrailingBlankContext guards the seeding bug where
// capturing the staged diff through the trimming run() stripped a hunk's
// trailing blank context lines, corrupting the patch ("corrupt patch at line
// N") when git apply re-checked the line counts. The diff must be captured
// byte-exact.
func TestCreateWorktreeFromHead_TrailingBlankContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "t@t.co")
	gitRun("config", "user.name", "t")

	// A file whose final hunk will carry trailing blank context lines, plus a
	// file with no terminating newline — both patch shapes the trim corrupted.
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("f.txt", "alpha\nbeta\ngamma\n\n\n")
	write("nonl.txt", "x") // no trailing newline
	gitRun("add", ".")
	gitRun("commit", "-m", "init")

	// Stage edits that produce trailing-blank-context and no-newline hunks.
	write("f.txt", "alpha\nBETA\ngamma\n\n\n")
	write("nonl.txt", "xy")
	gitRun("add", ".")

	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatalf("seeding must not corrupt the staged patch: %v", err)
	}
	defer wt.Remove()

	// The worktree must carry the staged edits.
	got, err := os.ReadFile(filepath.Join(wt.Dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nBETA\ngamma\n\n\n" {
		t.Errorf("seeded worktree missing staged edit: %q", got)
	}
}

// TestCreateWorktreeFromHead_StagedBinary guards the bug where the staged diff
// was captured/applied without --binary, so seeding a worktree failed on a
// staged binary file ("cannot apply binary patch … without full index line").
func TestCreateWorktreeFromHead_StagedBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "t@t.co")
	gitRun("config", "user.name", "t")
	gitRun("commit", "--allow-empty", "-m", "init")

	// Stage a new binary file (bytes that aren't valid UTF-8 text).
	want := []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0xff, 0xfe, 0x0a, 0x00}
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")

	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatalf("seeding a staged binary file must succeed: %v", err)
	}
	defer wt.Remove()

	got, err := os.ReadFile(filepath.Join(wt.Dir, "logo.png"))
	if err != nil {
		t.Fatalf("staged binary not present in the seeded worktree: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("binary round-trip corrupted: got %v want %v", got, want)
	}
}

// TestCreateWorktree_LinksNodeModules guards the JS-monorepo gate: a git
// worktree only holds tracked files, so gitignored node_modules is absent and
// tsc/eslint fail with "command not found". The worktree must symlink each
// node_modules from the live checkout — including nested ones — so JS steps
// resolve their deps without a reinstall.
func TestCreateWorktree_LinksNodeModules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "t@t.co")
	gitRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Root + nested (web/) node_modules with a marker binary, all gitignored.
	for _, nm := range []string{"node_modules", filepath.Join("web", "node_modules")} {
		bin := filepath.Join(dir, nm, ".bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "tsc"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "app.ts"), []byte("export {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-m", "init")

	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatalf("CreateWorktreeFromHead: %v", err)
	}
	defer wt.Remove()

	for _, nm := range []string{"node_modules", filepath.Join("web", "node_modules")} {
		// The default exposes deps as a symlink (fast, O(1)).
		fi, err := os.Lstat(filepath.Join(wt.Dir, nm))
		if err != nil {
			t.Fatalf("worktree missing %s: %v", nm, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink by default, got mode %v", nm, fi.Mode())
		}
		// The tsc marker must be reachable through the worktree's linked node_modules.
		if _, err := os.Stat(filepath.Join(wt.Dir, nm, ".bin", "tsc")); err != nil {
			t.Errorf("worktree missing linked %s (tsc unreachable): %v", nm, err)
		}
	}
}

// TestCreateWorktree_MaterializesNodeModules guards the Turbopack fix: with
// materializeDeps=true, node_modules must be a REAL directory in the worktree
// (not a symlink pointing out of the filesystem root), so Next.js 16/Turbopack
// accepts it — while its contents remain reachable for the JS steps.
func TestCreateWorktree_MaterializesNodeModules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init")
	gitRun("config", "user.email", "t@t.co")
	gitRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// node_modules with a regular marker file and an internal .bin symlink
	// (mirrors a real install: .bin/tsc → ../typescript/bin/tsc).
	bin := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "marker"), []byte("dep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "marker"), filepath.Join(bin, "tsc")); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-m", "init")

	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(true)
	if err != nil {
		t.Fatalf("CreateWorktreeFromHead: %v", err)
	}
	defer wt.Remove()

	// node_modules must be a real directory, NOT a symlink.
	fi, err := os.Lstat(filepath.Join(wt.Dir, "node_modules"))
	if err != nil {
		t.Fatalf("worktree missing materialized node_modules: %v", err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("node_modules should be a real directory, got mode %v", fi.Mode())
	}
	// The regular file is materialized (hardlink or copy) as a real file.
	mfi, err := os.Lstat(filepath.Join(wt.Dir, "node_modules", "marker"))
	if err != nil || mfi.Mode()&os.ModeSymlink != 0 || !mfi.Mode().IsRegular() {
		t.Fatalf("marker should be a real regular file: mode=%v err=%v", mfi.Mode(), err)
	}
	// The internal symlink is preserved and still resolves.
	lfi, err := os.Lstat(filepath.Join(wt.Dir, "node_modules", ".bin", "tsc"))
	if err != nil || lfi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".bin/tsc should stay a symlink: mode=%v err=%v", lfi.Mode(), err)
	}
	if b, err := os.ReadFile(filepath.Join(wt.Dir, "node_modules", ".bin", "tsc")); err != nil || string(b) != "dep\n" {
		t.Fatalf("materialized symlink should resolve to the marker: %q err=%v", b, err)
	}
}
