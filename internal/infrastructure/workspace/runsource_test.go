package workspace

import (
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	gitinfra "go.klarlabs.de/warden/internal/infrastructure/git"
)

// The affected selection compares the configuration digest committed at HEAD
// with the one the run loaded from the working copy. Equal files must digest
// equally, or every pre-commit would silently run every workspace.
func TestRunSource(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "config", "user.email", "warden-test")
	gitT(t, dir, "config", "user.name", "warden-test")
	write(t, dir, ".warden.yaml", "steps: { pre_commit: [test] }\nworkspaces: { discover: [js], select: affected }\nper_workspace:\n  test: { each: \"true\" }\n")
	write(t, dir, "package.json", `{"name":"root","workspaces":["packages/*"]}`)
	write(t, dir, "packages/a/package.json", `{"name":"a"}`)
	gitT(t, dir, "add", "--", ".warden.yaml", "package.json", "packages")
	gitT(t, dir, "commit", "-q", "-m", "fixture")
	head := strings.TrimSpace(gitT(t, dir, "rev-parse", "HEAD"))

	repo, err := gitinfra.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := NewRunSource(repo)

	loaded, err := config.NewRepository(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := domain.DigestJSON(loaded)
	got, err := src.ConfigurationDigestAt(head)
	if err != nil || got != want {
		t.Fatalf("digest at HEAD %q (%v), loaded %q", got, err, want)
	}

	write(t, dir, "packages/a/index.ts", "x\n")
	gitT(t, dir, "add", "--", "packages/a/index.ts")
	tree, err := src.TrackedTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := src.ChangedPaths(head, tree)
	if err != nil || !reflect.DeepEqual(changed, []string{"packages/a/index.ts"}) {
		t.Fatalf("changed %v (%v)", changed, err)
	}
	g, err := src.Discover(tree, []string{"js"})
	if err != nil || !reflect.DeepEqual(g.Paths(), []string{"packages/a"}) {
		t.Fatalf("graph %v (%v)", g.Paths(), err)
	}
	if _, err := src.Discover(tree, nil); err == nil {
		t.Fatal("a run naming no ecosystem must not discover every one")
	}
	if _, err := src.ConfigurationDigestAt("no-such-rev"); err != nil {
		// A missing ref reads as no config; either way it must not equal HEAD's.
		return
	}
	if d, _ := src.ConfigurationDigestAt("no-such-rev"); d == want {
		t.Fatal("an unreadable revision must not digest like HEAD")
	}
}
