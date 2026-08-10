package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		locked     map[string]string // committed lockfile, in the worktree
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
			wt, live := t.TempDir(), t.TempDir()
			writeLock(t, filepath.Join(wt, "package-lock.json"), tc.locked)
			if !tc.noHidden {
				writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), tc.installed)
			}

			got := DetectDepDrift(wt, live)

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
	wt, live := t.TempDir(), t.TempDir()

	writeLock(t, filepath.Join(wt, "web", "package-lock.json"), map[string]string{"node_modules/a": "2.0.0"})
	writeLock(t, filepath.Join(live, "web", "node_modules", ".package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})

	writeLock(t, filepath.Join(wt, "site", "package-lock.json"), map[string]string{"node_modules/b": "1.0.0"})
	writeLock(t, filepath.Join(live, "site", "node_modules", ".package-lock.json"), map[string]string{"node_modules/b": "1.0.0"})

	got := DetectDepDrift(wt, live)
	if len(got) != 1 {
		t.Fatalf("got %d drifts, want 1 (web only)", len(got))
	}
	if got[0].Lockfile != filepath.Join("web", "package-lock.json") {
		t.Errorf("drift reported for %q, want web/package-lock.json", got[0].Lockfile)
	}
}

// The walk must not descend into node_modules: a dependency ships its own
// package-lock.json, and treating those as the project's would report drift
// for packages the project never declared.
func TestDetectDepDriftIgnoresLockfilesInsideNodeModules(t *testing.T) {
	wt, live := t.TempDir(), t.TempDir()

	writeLock(t, filepath.Join(wt, "package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})
	writeLock(t, filepath.Join(live, "node_modules", ".package-lock.json"), map[string]string{"node_modules/a": "1.0.0"})
	// A vendored lockfile that must be ignored.
	writeLock(t, filepath.Join(wt, "node_modules", "dep", "package-lock.json"), map[string]string{"node_modules/x": "9.9.9"})

	if got := DetectDepDrift(wt, live); len(got) != 0 {
		t.Fatalf("reported drift from a lockfile inside node_modules: %v", got[0].Summary())
	}
}

// A wall of package names is a warning nobody reads.
func TestDepDriftExamplesAreBounded(t *testing.T) {
	d := DepDrift{Lockfile: "package-lock.json"}
	for i := 0; i < 20; i++ {
		d.Missing = append(d.Missing, string(rune('a'+i)))
	}

	ex := d.Examples()
	if len(ex) != maxDriftExamples+1 {
		t.Fatalf("got %d examples, want %d plus a summary line", len(ex), maxDriftExamples)
	}
	if !strings.Contains(ex[len(ex)-1], "more") {
		t.Errorf("truncated list does not say how many were omitted: %v", ex)
	}
}
