package git

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigValueAndRemoteURL(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}

	if got := repo.ConfigValue("warden.nonexistent"); got != "" {
		t.Errorf("unset key = %q, want empty", got)
	}
	if got := repo.RemoteURL("origin"); got != "" {
		t.Errorf("RemoteURL with no remote = %q, want empty (local-only checkout is not an error)", got)
	}

	gitRun(t, dir, "config", "warden.probe", "value-1")
	if got := repo.ConfigValue("warden.probe"); got != "value-1" {
		t.Errorf("ConfigValue = %q, want value-1", got)
	}
	gitRun(t, dir, "remote", "add", "origin", "https://example.invalid/acme/repo.git")
	if got := repo.RemoteURL("origin"); got != "https://example.invalid/acme/repo.git" {
		t.Errorf("RemoteURL = %q", got)
	}
	if got := repo.RemoteURL("upstream"); got != "" {
		t.Errorf("RemoteURL of a missing remote = %q, want empty", got)
	}
}

func TestRefsPointingAt(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	refs, err := repo.RefsPointingAt("")
	if err != nil || refs != nil {
		t.Fatalf("empty sha: refs=%v err=%v, want nil,nil", refs, err)
	}

	gitRun(t, dir, "branch", "zeta")
	gitRun(t, dir, "-c", "tag.gpgSign=false", "tag", "--no-sign", "v1")
	want := []string{"refs/heads/main", "refs/heads/zeta", "refs/tags/v1"}
	got, err := repo.RefsPointingAt(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs = %v, want %v (sorted, unique, heads+tags)", got, want)
	}

	// Ordinary mid-branch commit: no refs, and that is not an error.
	next := commitFile(t, dir, "next.txt")
	_ = commitFile(t, dir, "tip.txt")
	got, err = repo.RefsPointingAt(next)
	if err != nil || len(got) != 0 {
		t.Errorf("mid-branch commit: refs=%v err=%v, want none", got, err)
	}
}

func TestPushAnchorRequiresAndPublishesTheAnchor(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)
	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.PushAnchor("origin", sha); err != nil {
		t.Fatalf("PushAnchor: %v", err)
	}
	if got := gitRev(t, bare, AnchorRefPrefix+sha); got != sha {
		t.Errorf("remote anchor = %s, want %s", got, sha)
	}

	if err := repo.PushAnchor("origin", strings.Repeat("0", 40)); err == nil {
		t.Error("PushAnchor of a nonexistent commit must fail, not silently succeed")
	}
}

func TestWorktreeTreeStateTracksIndexAndUnstagedChanges(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	wt, err := repo.CreateWorktreeFromHead(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wt.Remove() })

	clean, err := wt.TreeState()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := wt.TreeState()
	if clean != again {
		t.Fatalf("TreeState must be stable on an untouched tree: %v vs %v", clean, again)
	}

	readme := filepath.Join(wt.Dir, "README.md")
	if err := os.WriteFile(readme, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unstaged, err := wt.TreeState()
	if err != nil {
		t.Fatal(err)
	}
	if unstaged.Unstaged == clean.Unstaged || unstaged.Index != clean.Index || unstaged.Head != clean.Head {
		t.Errorf("an unstaged edit must change only Unstaged: clean=%v unstaged=%v", clean, unstaged)
	}

	gitRun(t, wt.Dir, "add", "README.md")
	staged, err := wt.TreeState()
	if err != nil {
		t.Fatal(err)
	}
	if staged.Index == clean.Index {
		t.Error("staging must change the Index tree")
	}
}
