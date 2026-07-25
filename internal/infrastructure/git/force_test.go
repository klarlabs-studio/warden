package git

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// amendMain rewrites main's tip in place, producing the shape a rebase or an
// amend leaves behind: a new SHA whose history no longer descends from what the
// remote has.
func amendMain(t *testing.T, dir, msg string) string {
	t.Helper()
	gitRun(t, dir, "commit", "-q", "--allow-empty", "--amend", "--no-verify", "-m", msg)
	return gitRev(t, dir, "main")
}

func TestPushRewritesHistory(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	setupBareRemote(t, dir)

	t.Run("an ordinary advance is not a rewrite", func(t *testing.T) {
		gitRun(t, dir, "commit", "-q", "--allow-empty", "--no-verify", "-m", "advance")
		rewrites, err := repo.PushRewritesHistory("origin", "main")
		if err != nil {
			t.Fatal(err)
		}
		if rewrites {
			t.Error("a fast-forward must not be reported as a rewrite")
		}
	})

	t.Run("an amended tip is a rewrite", func(t *testing.T) {
		// Publish first, so the remote-tracking ref points AT the commit we are
		// about to rewrite. (Amending an unpushed commit leaves the remote tip an
		// ancestor, which is correctly not a rewrite — see the subtest above.)
		gitRun(t, dir, "push", "-q", "origin", "main")
		amendMain(t, dir, "rewritten")
		rewrites, err := repo.PushRewritesHistory("origin", "main")
		if err != nil {
			t.Fatal(err)
		}
		if !rewrites {
			t.Error("a rewritten tip must be detected as a rewrite")
		}
	})

	t.Run("a branch that was never pushed rewrites nothing", func(t *testing.T) {
		gitRun(t, dir, "branch", "brand-new", "main")
		rewrites, err := repo.PushRewritesHistory("origin", "brand-new")
		if err != nil {
			t.Fatal(err)
		}
		if rewrites {
			t.Error("no remote-tracking ref means there is nothing to discard")
		}
	})
}

// The whole point of the lease: it must be pinned to what this clone last
// FETCHED, not to the remote's live value. Pinning to the live value would make
// the lease vacuously true and silently degrade it to a bare --force.
func TestPush_LeaseRefusesToClobberUnfetchedWork(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)

	// A second clone pushes a commit our clone has never fetched.
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", bare, ".")
	gitRun(t, other, "config", "user.email", "o@o.co")
	gitRun(t, other, "config", "user.name", "o")
	gitRun(t, other, "commit", "-q", "--allow-empty", "--no-verify", "-m", "someone else's work")
	gitRun(t, other, "push", "-q", "origin", "main")
	theirs := gitRev(t, other, "main")

	// We rewrite our tip and push. Our remote-tracking ref is stale, so the lease
	// must fail rather than discard work we have not seen.
	amendMain(t, dir, "my rewrite")
	if err := repo.Push("origin", "main", domain.ForceLease); err == nil {
		t.Fatal("the lease must refuse a rewrite over unfetched work")
	}
	if got := gitRev(t, bare, "refs/heads/main"); got != theirs {
		t.Errorf("remote main = %s, want the other clone's commit %s — their work was discarded", got, theirs)
	}
}

// With the remote-tracking ref current, the same rewrite is exactly what the
// developer asked for and must succeed — otherwise the only way to push a
// rebased branch is `--no-verify`, which skips the gate and writes no
// provenance (the failure this whole path exists to remove).
func TestPush_LeaseRewritesWhatWeHaveSeen(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)

	rewritten := amendMain(t, dir, "my rewrite")
	if err := repo.Push("origin", "main", domain.ForceLease); err != nil {
		t.Fatalf("Push(lease): %v", err)
	}
	if got := gitRev(t, bare, "refs/heads/main"); got != rewritten {
		t.Errorf("remote main = %s, want the rewritten tip %s", got, rewritten)
	}
}

// ForceNever must leave git to refuse, exactly as it would without warden.
func TestPush_NeverLeavesGitToRefuse(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)
	before := gitRev(t, bare, "refs/heads/main")

	amendMain(t, dir, "my rewrite")
	if err := repo.Push("origin", "main", domain.ForceNever); err == nil {
		t.Fatal("a non-fast-forward push must fail without a force")
	}
	if got := gitRev(t, bare, "refs/heads/main"); got != before {
		t.Errorf("remote main = %s, want it untouched at %s", got, before)
	}
}

// A fast-forward under ForceLease must still land; the lease is a safety net,
// not a restriction on ordinary pushes.
func TestPush_LeaseStillAllowsFastForward(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	bare := setupBareRemote(t, dir)

	gitRun(t, dir, "commit", "-q", "--allow-empty", "--no-verify", "-m", "advance")
	advanced := gitRev(t, dir, "main")
	if err := repo.Push("origin", "main", domain.ForceLease); err != nil {
		t.Fatalf("Push(lease) on a fast-forward: %v", err)
	}
	if got := gitRev(t, bare, "refs/heads/main"); got != advanced {
		t.Errorf("remote main = %s, want %s", got, advanced)
	}
}

func TestPushSpanBase(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	setupBareRemote(t, dir)
	onRemote := gitRev(t, dir, "main")

	t.Run("names the newest commit the remote already has", func(t *testing.T) {
		gitRun(t, dir, "commit", "-q", "--allow-empty", "--no-verify", "-m", "A")
		gitRun(t, dir, "commit", "-q", "--allow-empty", "--no-verify", "-m", "B")
		base, err := repo.PushSpanBase("origin", "main")
		if err != nil {
			t.Fatal(err)
		}
		if base != onRemote {
			t.Errorf("PushSpanBase = %s, want %s", base, onRemote)
		}
		// The span is exactly what this push publishes.
		span, err := repo.CommitsInSpan(base, gitRev(t, dir, "main"))
		if err != nil {
			t.Fatal(err)
		}
		if len(span) != 2 {
			t.Errorf("span = %v, want the two unpushed commits", span)
		}
	})

	t.Run("survives a rebase by landing on the merge-base", func(t *testing.T) {
		// Rebasing makes every commit new to the remote; the boundary must still
		// be a commit the remote has, not an empty result.
		gitRun(t, dir, "commit", "-q", "--allow-empty", "--amend", "--no-verify", "-m", "B rewritten")
		base, err := repo.PushSpanBase("origin", "main")
		if err != nil {
			t.Fatal(err)
		}
		if base != onRemote {
			t.Errorf("PushSpanBase after rewrite = %s, want the merge-base %s", base, onRemote)
		}
	})
}

func TestCommitsInSpan_EmptyBaseCoversNothing(t *testing.T) {
	dir := newTestRepo(t)
	repo := &Repo{Dir: dir}
	// A span warden could not determine must cover nothing rather than
	// everything — the fail-safe direction.
	for _, args := range [][2]string{{"", "main"}, {"main", ""}, {"", ""}} {
		got, err := repo.CommitsInSpan(args[0], args[1])
		if err != nil {
			t.Fatalf("CommitsInSpan(%q, %q): %v", args[0], args[1], err)
		}
		if len(got) != 0 {
			t.Errorf("CommitsInSpan(%q, %q) = %v, want nothing", args[0], args[1], got)
		}
	}
}
