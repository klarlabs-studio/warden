package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/policy"
)

// The changed-path set feeds two things that must see real paths: match.paths
// rules and WARDEN_CHANGED_FILES. git's default diff output is neither. A
// rename comes out as `dir/{a => b}/f` and a path with non-ASCII bytes comes
// out C-quoted ("odd dir/na\303\257ve.txt"), and no glob matches either form.
// These tests build a change with a cross-directory rename, a deletion and an
// oddly named file, and assert the exact path list git's raw object paths give.

// gitIn runs git in dir and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const oddPath = "odd dir/naïve file (1).txt"

// stageRenameDeleteOdd stages, on top of a base commit, a rename across
// directories, a deletion and a new file whose name needs quoting. It returns
// the base commit's SHA.
func stageRenameDeleteOdd(t *testing.T) (dir, base string) {
	t.Helper()
	dir = newTestRepo(t)
	// Enough identical lines that git's rename detection pairs the two paths;
	// without --no-renames this is exactly the case that yields `{a => b}`.
	body := strings.Repeat("package sub // same line\n", 20)
	writeRepoFile(t, dir, "old/sub/f.go", body)
	writeRepoFile(t, dir, "gone.txt", "bye\n")
	gitIn(t, dir, "add", "old/sub/f.go", "gone.txt")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	// Pin the defaults the bug depends on, whatever the machine's global config.
	gitIn(t, dir, "config", "diff.renames", "true")
	gitIn(t, dir, "config", "core.quotePath", "true")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(string(out))

	if err := os.MkdirAll(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "mv", "old/sub", "new/sub")
	gitIn(t, dir, "rm", "-q", "gone.txt")
	writeRepoFile(t, dir, oddPath, "one\ntwo\n")
	gitIn(t, dir, "add", oddPath)
	return dir, base
}

var wantChangedPaths = []string{"gone.txt", "new/sub/f.go", oddPath, "old/sub/f.go"}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func TestDiffStats_ReportsRealPathsForRenameDeleteAndQuotedNames(t *testing.T) {
	dir, base := stageRenameDeleteOdd(t)
	gitIn(t, dir, "commit", "-q", "-m", "change")
	repo := &Repo{Dir: dir}

	stats, err := repo.DiffStats(base)
	if err != nil {
		t.Fatalf("DiffStats: %v", err)
	}
	if got := sortedCopy(stats.Paths); !reflect.DeepEqual(got, wantChangedPaths) {
		t.Fatalf("DiffStats paths = %q, want %q", got, wantChangedPaths)
	}
	if stats.FilesTouched != len(wantChangedPaths) {
		t.Errorf("FilesTouched = %d, want %d", stats.FilesTouched, len(wantChangedPaths))
	}
	// Line counts still come through: 20 deleted + 20 added for the rename,
	// 1 deleted for gone.txt, 2 added for the new file.
	if stats.LinesChanged != 43 {
		t.Errorf("LinesChanged = %d, want 43", stats.LinesChanged)
	}
	assertPathRulesFire(t, stats.Paths)
}

func TestStagedPaths_ReportsRealPathsForRenameDeleteAndQuotedNames(t *testing.T) {
	dir, _ := stageRenameDeleteOdd(t)
	repo := &Repo{Dir: dir}

	stats, err := repo.StagedPaths()
	if err != nil {
		t.Fatalf("StagedPaths: %v", err)
	}
	if got := sortedCopy(stats.Paths); !reflect.DeepEqual(got, wantChangedPaths) {
		t.Fatalf("StagedPaths = %q, want %q", got, wantChangedPaths)
	}
	if stats.FilesTouched != len(wantChangedPaths) {
		t.Errorf("FilesTouched = %d, want %d", stats.FilesTouched, len(wantChangedPaths))
	}
	assertPathRulesFire(t, stats.Paths)
}

// assertPathRulesFire resolves a policy whose rules match the rename's old
// directory, its new directory and the oddly named file, and asserts each one
// fires on the computed path set.
func assertPathRulesFire(t *testing.T, paths []string) {
	t.Helper()
	cfg := domain.Config{
		Steps: map[string][]domain.StepName{"pre_push": {"lint"}},
		Rules: []domain.Rule{
			{Match: domain.Match{Paths: []string{"old/**"}}},
			{Match: domain.Match{Paths: []string{"new/**"}}},
			{Match: domain.Match{Paths: []string{"odd dir/*.txt"}}},
		},
	}
	got := policy.Resolve(cfg, policy.Input{Hook: domain.PrePush, Branch: "feature", Paths: paths, Risk: domain.RiskLow})
	for _, want := range []string{"paths=old/**", "paths=new/**", "paths=odd dir/*.txt"} {
		if !slices.Contains(got.MatchedRules, want) {
			t.Errorf("rule %q did not fire on %q; matched %q", want, paths, got.MatchedRules)
		}
	}
}

func TestParseNumstatZ(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		paths []string
		lines int
	}{
		{"empty", "", nil, 0},
		{"plain", "3\t1\ta.go\x00", []string{"a.go"}, 4},
		{"tab and space in path", "1\t0\tdir x/a\tb.txt\x00", []string{"dir x/a\tb.txt"}, 1},
		{"binary", "-\t-\timg.png\x00", []string{"img.png"}, 0},
		// --no-renames means git should never emit this, but a rename record
		// (empty path column, then old and new) must still yield both real paths.
		{"rename record", "2\t1\t\x00old/a.go\x00new/a.go\x001\t1\tz\x00", []string{"old/a.go", "new/a.go", "z"}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNumstatZ(tt.in)
			if !reflect.DeepEqual(got.Paths, tt.paths) {
				t.Errorf("paths = %q, want %q", got.Paths, tt.paths)
			}
			if got.LinesChanged != tt.lines {
				t.Errorf("lines = %d, want %d", got.LinesChanged, tt.lines)
			}
			if got.FilesTouched != len(tt.paths) {
				t.Errorf("files = %d, want %d", got.FilesTouched, len(tt.paths))
			}
		})
	}
}
