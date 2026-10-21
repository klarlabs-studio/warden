package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func anchorGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	anchorGit(t, dir, "add", name)
	anchorGit(t, dir, "commit", "-q", "-m", name)
	return anchorGit(t, dir, "rev-parse", "HEAD")
}

func noteOn(t *testing.T, repo *Repo, sha, runID string) {
	t.Helper()
	if err := repo.WriteNote(sha, domain.RunRecord{RunID: runID, CommitSHA: sha}); err != nil {
		t.Fatal(err)
	}
}

// The walk follows first parents only, excludes the commit it starts from,
// passes over notes accept refuses, and reports each candidate's real tree.
func TestFirstNotedAncestor(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	root := anchorGit(t, dir, "rev-parse", "HEAD")
	a := commitFile(t, dir, "a")
	// A side branch whose commit carries a note: reachable only through the
	// merge's second parent, so never offered.
	anchorGit(t, dir, "switch", "-q", "-c", "side")
	side := commitFile(t, dir, "side")
	anchorGit(t, dir, "switch", "-q", "main")
	b := commitFile(t, dir, "b")
	anchorGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := anchorGit(t, dir, "rev-parse", "HEAD")
	head := commitFile(t, dir, "c")

	noteOn(t, repo, root, "root")
	noteOn(t, repo, a, "a")
	noteOn(t, repo, side, "side")
	noteOn(t, repo, head, "head") // the starting commit: never offered

	var offered []string
	accept := func(want string) func(NotedCandidate) bool {
		return func(c NotedCandidate) bool {
			offered = append(offered, c.Record.RunID)
			tree, _ := repo.TreeSHA(c.SHA)
			if c.Tree != tree || c.Record.CommitSHA != c.SHA {
				t.Errorf("candidate %s: tree %s, want %s", c.SHA, c.Tree, tree)
			}
			return c.Record.RunID == want
		}
	}
	c, found, err := repo.FirstNotedAncestor(head, 500, accept("root"))
	if err != nil || !found || c.SHA != root {
		t.Fatalf("got %+v %v %v", c, found, err)
	}
	if want := []string{"a", "root"}; strings.Join(offered, ",") != strings.Join(want, ",") {
		t.Fatalf("offered %v, want %v (first parents only, start excluded, nearest first)", offered, want)
	}

	// The limit bounds the walk: head^ is the merge, then b, a, root.
	offered = nil
	if _, found, _ := repo.FirstNotedAncestor(head, 2, accept("root")); found || len(offered) != 0 {
		t.Fatalf("a walk of 2 (merge, b) must find nothing: found %v, offered %v", found, offered)
	}
	if _, found, _ := repo.FirstNotedAncestor(head, 0, accept("a")); found {
		t.Fatal("a zero limit walks nothing")
	}
	// A root commit has no ancestors.
	if _, found, err := repo.FirstNotedAncestor(root, 500, accept("root")); found || err != nil {
		t.Fatalf("root: %v %v", found, err)
	}
	_ = b
	_ = merge
}

// A repository without a notes ref has nothing to anchor on, and says so
// without an error.
func TestFirstNotedAncestor_NoNotes(t *testing.T) {
	dir := newTestRepo(t)
	commitFile(t, dir, "a")
	repo := &Repo{Dir: dir}
	if _, found, err := repo.FirstNotedAncestor("HEAD", 500, func(NotedCandidate) bool { return true }); found || err != nil {
		t.Fatalf("found %v err %v", found, err)
	}
}

// The adapter fetches the remote's notes first, so a note CI wrote can
// anchor; and the fetch never discards a local note the remote lacks.
func TestAdapterFullScopeAnchor_FetchesNotesWithoutForcing(t *testing.T) {
	dir := newTestRepo(t)
	remote := t.TempDir()
	anchorGit(t, remote, "init", "-q", "--bare")
	anchorGit(t, dir, "remote", "add", "origin", remote)
	base := commitFile(t, dir, "a")
	anchorGit(t, dir, "push", "-q", "origin", "main")

	// "CI" notes base in its own clone and publishes the note.
	ci := t.TempDir()
	anchorGit(t, ci, "clone", "-q", remote, ".")
	anchorGit(t, ci, "config", "user.email", "warden-ci")
	anchorGit(t, ci, "config", "user.name", "warden-ci")
	ciRepo := &Repo{Dir: ci}
	noteOn(t, ciRepo, base, "ci-full")
	anchorGit(t, ci, "push", "-q", "origin", NotesRef+":"+NotesRef)

	head := commitFile(t, dir, "b")
	a := NewAdapter(&Repo{Dir: dir})
	c, found, err := a.FullScopeAnchor("origin", head, 500, func(c application.AnchorCandidate) bool { return c.Record.RunID == "ci-full" })
	if err != nil || !found || c.SHA != base {
		t.Fatalf("the remote's note must be fetched and offered: %+v %v %v", c, found, err)
	}

	// A local note the remote does not have survives a later search: the
	// diverged fetch is refused, not forced.
	local := &Repo{Dir: dir}
	noteOn(t, local, head, "local-unpublished")
	other := commitFile(t, ci, "ci-only")
	noteOn(t, ciRepo, other, "ci-later")
	anchorGit(t, ci, "push", "-q", "origin", "main", NotesRef+":"+NotesRef)
	_, _, _ = a.FullScopeAnchor("origin", head, 500, func(application.AnchorCandidate) bool { return false })
	if rec, err := local.ReadNote(head); err != nil || rec == nil || rec.RunID != "local-unpublished" {
		t.Fatalf("the local note must survive the fetch: %+v %v", rec, err)
	}
	if tree, err := a.CommitTree(head); err != nil || tree != anchorGit(t, dir, "rev-parse", head+"^{tree}") {
		t.Fatalf("CommitTree = %q %v", tree, err)
	}
}
