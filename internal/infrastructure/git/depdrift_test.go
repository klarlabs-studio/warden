package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// writeLock writes a package-lock-shaped file with the given path→version map.
func writeLock(t *testing.T, path string, pkgs map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"lockfileVersion":3,"packages":{`)
	first := true
	for k, v := range pkgs {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + k + `":{"version":"` + v + `"}`)
	}
	b.WriteString(`}}`)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Warden exposes node_modules from the live checkout instead of reinstalling,
// so the tracked tree comes from the commit and the dependency tree comes from
// the machine. When those disagree, the checks run against dependencies the
// commit does not specify and CI can legitimately disagree — and warden could
// not see it (#204).
func TestDetectDepDrift(t *testing.T) {
	tests := []struct {
		name       string
		locked     map[string]string // lockfile committed at the attested commit
		installed  map[string]string // node_modules/.package-lock.json, live
		noHidden   bool              // simulate "never installed"
		wantDrift  bool
		wantSubstr string
	}{
		{
			name:      "install matches the lockfile",
			locked:    map[string]string{"node_modules/left-pad": "1.3.0"},
			installed: map[string]string{"node_modules/left-pad": "1.3.0"},
			wantDrift: false,
		},
		{
			// The branch-switch case: the lockfile moved, the install did not.
			name:       "installed at a different version",
			locked:     map[string]string{"node_modules/left-pad": "1.3.0"},
			installed:  map[string]string{"node_modules/left-pad": "1.2.0"},
			wantDrift:  true,
			wantSubstr: "different version",
		},
		{
			name:       "lockfile package not installed",
			locked:     map[string]string{"node_modules/left-pad": "1.3.0", "node_modules/dayjs": "1.11.0"},
			installed:  map[string]string{"node_modules/left-pad": "1.3.0"},
			wantDrift:  true,
			wantSubstr: "not installed",
		},
		{
			// An extra installed package is not drift: `npm link` and stray
			// installs do not change what the commit resolves to.
			name:      "extra installed packages are not drift",
			locked:    map[string]string{"node_modules/left-pad": "1.3.0"},
			installed: map[string]string{"node_modules/left-pad": "1.3.0", "node_modules/extra": "9.9.9"},
			wantDrift: false,
		},
		{
			// No install at all means nothing to compare. Reporting drift here
			// would fire on every fresh clone, and a warning that cries wolf
			// gets muted — which costs more than it saves.
			name:      "no hidden lockfile reports nothing",
			locked:    map[string]string{"node_modules/left-pad": "1.3.0"},
			noHidden:  true,
			wantDrift: false,
		},
		{
			// The root entry has no version and workspace links have none
			// either; neither is a mismatch.
			name:      "versionless entries are skipped",
			locked:    map[string]string{"": "", "node_modules/left-pad": "1.3.0"},
			installed: map[string]string{"node_modules/left-pad": "1.3.0"},
			wantDrift: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wt, live := newTestRepo(t), t.TempDir()
			writeLock(t, filepath.Join(wt, "package-lock.json"), tc.locked)
			head := commitPaths(t, wt, "package-lock.json")
			if !tc.noHidden {
				writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), tc.installed)
			}

			got := DetectDepDrift(wt, head, live)

			if tc.wantDrift && len(got) == 0 {
				t.Fatal("expected drift, got none")
			}
			if !tc.wantDrift && len(got) > 0 {
				t.Fatalf("expected no drift, got %v", got[0].Summary())
			}
			if tc.wantSubstr != "" {
				if s := got[0].Summary(); !strings.Contains(s, tc.wantSubstr) {
					t.Errorf("summary %q does not contain %q", s, tc.wantSubstr)
				}
			}
		})
	}
}

// A monorepo has a lockfile per workspace, each with its own install.
func TestDetectDepDriftFindsNestedLockfiles(t *testing.T) {
	wt, live := newTestRepo(t), t.TempDir()

	writeLock(t, filepath.Join(wt, "web", "package-lock.json"), map[string]string{"node_modules/a": "2.0.0"})
	writeLock(t, filepath.Join(live, "web", "node_modules", ".package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})

	writeLock(t, filepath.Join(wt, "site", "package-lock.json"), map[string]string{"node_modules/b": "1.0.0"})
	writeLock(t, filepath.Join(live, "site", "node_modules", ".package-lock.json"), map[string]string{"node_modules/b": "1.0.0"})
	head := commitPaths(t, wt, "web/package-lock.json", "site/package-lock.json")

	got := DetectDepDrift(wt, head, live)
	if len(got) != 1 {
		t.Fatalf("got %d drifts, want 1 (web only)", len(got))
	}
	// A repository path, '/'-separated on every OS: it is signed into the note.
	if got[0].Lockfile != "web/package-lock.json" {
		t.Errorf("drift reported for %q, want web/package-lock.json", got[0].Lockfile)
	}
}

// The walk must not descend into node_modules: a dependency ships its own
// package-lock.json, and treating those as the project's would report drift
// for packages the project never declared.
func TestDetectDepDriftIgnoresLockfilesInsideNodeModules(t *testing.T) {
	wt, live := newTestRepo(t), t.TempDir()

	writeLock(t, filepath.Join(wt, "package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})
	writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})
	// A vendored lockfile that must be ignored.
	writeLock(t, filepath.Join(wt, "node_modules", "dep", "package-lock.json"), map[string]string{"node_modules/x": "9.9.9"})
	head := commitPaths(t, wt, "package-lock.json", "node_modules/dep/package-lock.json")

	if got := DetectDepDrift(wt, head, live); len(got) != 0 {
		t.Fatalf("reported drift from a lockfile inside node_modules: %v", got[0].Summary())
	}
}

// Drift is signed into a note that attests a commit, so the lockfile it
// compares against must be the one that commit carries. Walking the worktree
// instead compared against whatever a step left on disk.
func TestDetectDepDriftReadsTheCommittedTreeNotTheWorktree(t *testing.T) {
	installed := map[string]string{"node_modules/left-pad": "1.2.0"}

	t.Run("an untracked lockfile a step generated is not reported", func(t *testing.T) {
		wt, live := newTestRepo(t), t.TempDir()
		writeRepoFile(t, wt, "README.md", "x\n")
		head := commitPaths(t, wt, "README.md")
		// A step ran `npm install` in web/ and left a lockfile nobody committed.
		writeLock(t, filepath.Join(wt, "web", "package-lock.json"), map[string]string{"node_modules/left-pad": "1.3.0"})
		writeLock(t, filepath.Join(live, "web", "node_modules", ".package-lock.json"), installed)

		if got := DetectDepDrift(wt, head, live); len(got) != 0 {
			t.Fatalf("drift reported for a lockfile the commit does not contain: %+v", got)
		}
	})

	t.Run("a committed lockfile a step rewrote is compared as committed", func(t *testing.T) {
		wt, live := newTestRepo(t), t.TempDir()
		writeLock(t, filepath.Join(wt, "package-lock.json"), map[string]string{"node_modules/left-pad": "1.3.0"})
		head := commitPaths(t, wt, "package-lock.json")
		// A step rewrote the lockfile to match the install, hiding the drift
		// between what the commit specifies and what the checks ran against.
		writeLock(t, filepath.Join(wt, "package-lock.json"), installed)
		writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), installed)

		got := DetectDepDrift(wt, head, live)
		if len(got) != 1 || got[0].Lockfile != "package-lock.json" {
			t.Fatalf("drift = %+v, want the committed package-lock.json against the install", got)
		}
		if want := "node_modules/left-pad: 1.2.0 != 1.3.0"; len(got[0].Mismatched) != 1 || got[0].Mismatched[0] != want {
			t.Errorf("mismatched = %q, want [%q]", got[0].Mismatched, want)
		}
	})

	t.Run("no commit, no claim", func(t *testing.T) {
		wt, live := newTestRepo(t), t.TempDir()
		writeLock(t, filepath.Join(wt, "package-lock.json"), map[string]string{"node_modules/left-pad": "1.3.0"})
		_ = commitPaths(t, wt, "package-lock.json")
		writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), installed)
		for _, rev := range []string{"", "no-such-ref", "0000000000000000000000000000000000000000"} {
			if got := DetectDepDrift(wt, rev, live); len(got) != 0 {
				t.Errorf("DetectDepDrift(rev=%q) = %+v, want none", rev, got)
			}
		}
	})
}

// commitPaths stages and commits the given repository paths and returns the
// new commit's SHA.
func commitPaths(t *testing.T, dir string, paths ...string) string {
	t.Helper()
	gitIn(t, dir, append([]string{"add", "--"}, paths...)...)
	gitIn(t, dir, "commit", "-q", "-m", "lockfiles")
	return gitOutT(t, dir, "rev-parse", "HEAD")
}

// A wall of package names is a warning nobody reads.
func TestDepDriftExamplesAreBounded(t *testing.T) {
	d := domain.DepDrift{Lockfile: "package-lock.json"}
	for i := 0; i < 20; i++ {
		d.Missing = append(d.Missing, string(rune('a'+i)))
	}

	ex := d.Examples()
	if len(ex) != domain.MaxDriftExamples+1 {
		t.Fatalf("got %d examples, want %d plus a summary line", len(ex), domain.MaxDriftExamples)
	}
	if !strings.Contains(ex[len(ex)-1], "more") {
		t.Errorf("truncated list does not say how many were omitted: %v", ex)
	}
}
