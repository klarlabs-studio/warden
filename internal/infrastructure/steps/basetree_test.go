package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBaseSHA(t *testing.T) {
	ctx := context.Background()

	t.Run("uses the branch's upstream merge-base", func(t *testing.T) {
		dir := newGitRepo(t)
		writeFile(t, dir, "a.txt", "a")
		commitAll(t, dir, "base")
		base := revParse(t, dir, "HEAD")

		runGit(t, dir, "checkout", "-q", "-b", "feature")
		runGit(t, dir, "branch", "--set-upstream-to=main", "feature")
		writeFile(t, dir, "b.txt", "b")
		commitAll(t, dir, "work")

		got, err := resolveBaseSHA(ctx, dir, "", "")
		if err != nil {
			t.Fatalf("resolveBaseSHA: %v", err)
		}
		if got != base {
			t.Errorf("base = %s, want the merge-base %s", got, base)
		}
	})

	t.Run("an explicit ref wins", func(t *testing.T) {
		dir := newGitRepo(t)
		first := revParse(t, dir, "HEAD")
		writeFile(t, dir, "a.txt", "a")
		commitAll(t, dir, "second")

		got, err := resolveBaseSHA(ctx, dir, "", first)
		if err != nil {
			t.Fatalf("resolveBaseSHA: %v", err)
		}
		if got != first {
			t.Errorf("base = %s, want %s", got, first)
		}
	})

	t.Run("a typo'd explicit ref is an error, not a silent fallback", func(t *testing.T) {
		// Falling back to a different base would quietly change what the gate
		// enforces.
		dir := newGitRepo(t)
		if _, err := resolveBaseSHA(ctx, dir, "", "refs/heads/nope"); err == nil {
			t.Error("an unresolvable configured base was accepted")
		}
	})

	t.Run("a detached worktree still finds the branch's upstream", func(t *testing.T) {
		// Warden validates in a DETACHED worktree, where `@{upstream}` resolves to
		// nothing. Without the branch name the gate would fall through to the
		// remote default branch — or to no base at all — and measure the delta
		// against the wrong tree.
		dir := newGitRepo(t)
		writeFile(t, dir, "a.txt", "a")
		commitAll(t, dir, "base")
		base := revParse(t, dir, "HEAD")

		runGit(t, dir, "checkout", "-q", "-b", "feature")
		runGit(t, dir, "branch", "--set-upstream-to=main", "feature")
		writeFile(t, dir, "b.txt", "b")
		commitAll(t, dir, "work")

		wt := filepath.Join(t.TempDir(), "wt")
		runGit(t, dir, "worktree", "add", "--detach", "-q", wt, "HEAD")
		t.Cleanup(func() { runGit(t, dir, "worktree", "remove", "--force", wt) })

		if _, err := resolveBaseSHA(ctx, wt, "", ""); !errors.Is(err, errNoBase) {
			t.Fatalf("a detached worktree with no branch name should find no base, got %v", err)
		}
		got, err := resolveBaseSHA(ctx, wt, "feature", "")
		if err != nil {
			t.Fatalf("resolveBaseSHA: %v", err)
		}
		if got != base {
			t.Errorf("base = %s, want the feature branch's upstream merge-base %s", got, base)
		}
	})

	t.Run("no upstream and no remote reports no base", func(t *testing.T) {
		dir := newGitRepo(t)
		_, err := resolveBaseSHA(ctx, dir, "", "")
		if !errors.Is(err, errNoBase) {
			t.Errorf("err = %v, want errNoBase", err)
		}
	})
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitOut(context.Background(), dir, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v: %s", ref, err, out)
	}
	return strings.TrimSpace(out)
}

func TestMaterializeTree(t *testing.T) {
	dir := newGitRepo(t)
	writeFile(t, dir, "src/app.go", "package app")
	writeFile(t, dir, ".nox/baseline.json", `{"entries":[]}`)
	commitAll(t, dir, "base")
	base := revParse(t, dir, "HEAD")

	// Change the tree after the commit: the materialized copy must reflect the
	// commit, not the working tree, or the delta compares HEAD against itself.
	writeFile(t, dir, "src/app.go", "package app // edited")
	writeFile(t, dir, "untracked.txt", "not committed")

	dest := t.TempDir()
	if err := materializeTree(context.Background(), dir, base, dest); err != nil {
		t.Fatalf("materializeTree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "src", "app.go"))
	if err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	if string(got) != "package app" {
		t.Errorf("materialized content = %q, want the committed version", got)
	}
	// The committed baseline must come along, or the base scan would report
	// findings the base commit had already accepted.
	if _, err := os.Stat(filepath.Join(dest, ".nox", "baseline.json")); err != nil {
		t.Errorf("the committed baseline was not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "untracked.txt")); err == nil {
		t.Error("an uncommitted file leaked into the base tree")
	}
}

func TestExtractTar_RejectsEscapingEntries(t *testing.T) {
	// A tar entry that climbs out of the destination would let a crafted archive
	// write anywhere warden can write.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractTar(&buf, dest); err == nil {
		t.Fatal("an escaping tar entry was extracted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); err == nil {
		t.Error("the escaping entry was written outside the destination")
	}
}

func TestSafeJoin(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"src/app.go", true},
		{"./a.go", true},
		{"../escape", false},
		{"/etc/passwd", false},
		{"a/../../escape", false},
	}
	for _, c := range cases {
		if _, ok := safeJoin("/tmp/dest", c.name); ok != c.ok {
			t.Errorf("safeJoin(%q) ok = %v, want %v", c.name, ok, c.ok)
		}
	}
}
