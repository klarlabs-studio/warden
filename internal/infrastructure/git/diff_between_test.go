package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffStatsBetween(t *testing.T) {
	dir := newTestRepo(t)
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v: %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := run("rev-parse", "HEAD")
	if e := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "README.md")
	run("commit", "-m", "change")
	commit := run("rev-parse", "HEAD")
	run("checkout", "--detach", base)
	r := &Repo{Dir: dir}
	s, e := r.DiffStatsBetween(base, commit)
	if e != nil || s.FilesTouched != 1 || s.LinesChanged != 2 || len(s.Paths) != 1 || s.Paths[0] != "README.md" {
		t.Fatalf("stats: %+v %v", s, e)
	}
	if _, e = r.DiffStatsBetween("HEAD", commit); e == nil {
		t.Fatal("accepted mutable base")
	}
	if _, e = r.DiffStatsBetween(base, strings.Repeat("f", 40)); e == nil {
		t.Fatal("accepted missing object")
	}
}
