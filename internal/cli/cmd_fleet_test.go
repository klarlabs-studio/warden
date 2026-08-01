package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// The load-bearing distinction in this command. A squash-merge unbinds a note
// while the CONTENT was gated under the pre-squash commit, so counting those as
// bypasses inflates the very number that is supposed to trigger an intervention.
// Overstating the problem gets the metric dismissed as noisy, which costs more
// than not having it at all.
func TestCountBypassed_ExcludesRecoverableGaps(t *testing.T) {
	report := domain.AuditReport{Commits: []domain.CommitStatus{
		{SHA: "a", HasNote: true},               // gated
		{SHA: "b"},                              // genuinely bypassed
		{SHA: "c", ReattestableFrom: "gated99"}, // squash-merge: NOT a bypass
		{SHA: "d"},                              // genuinely bypassed
		{SHA: "e", HasNote: true, ChainIntact: false}, // tampered, but noted
	}}
	if got := countBypassed(report); got != 2 {
		t.Errorf("bypassed = %d, want 2 (reattestable and noted commits are not bypasses)", got)
	}
}

// "configured but never adopted" is a stalled adoption with a one-command fix
// and someone who already intended it. "not a warden repo" is not a problem at
// all. Reporting them identically buries the actionable one in a list of the
// irrelevant.
func TestFleet_SeparatesStalledAdoptionFromUnrelatedRepos(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	root := t.TempDir()

	// A repo carrying a config that was never adopted.
	stalled := filepath.Join(root, "stalled")
	mkRepo(t, stalled)
	if err := os.WriteFile(filepath.Join(stalled, ".warden.yaml"), []byte("steps: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A repo with no warden anything.
	mkRepo(t, filepath.Join(root, "unrelated"))

	rep := surveyFleet([]string{stalled, filepath.Join(root, "unrelated")}, "")

	if rep.Stalled != 1 {
		t.Errorf("stalled = %d, want 1", rep.Stalled)
	}
	if rep.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 (both are un-adopted)", rep.Skipped)
	}
	var sawConfigured, sawBare bool
	for _, r := range rep.Repos {
		if r.Configured && !r.Adopted {
			sawConfigured = true
		}
		if !r.Configured && !r.Adopted {
			sawBare = true
		}
	}
	if !sawConfigured || !sawBare {
		t.Errorf("the two un-adopted states must be distinguishable: %+v", rep.Repos)
	}
}

// A rollup that dies on the first bad checkout is useless at exactly the scale
// it exists for.
func TestFleet_OneBadRepoDoesNotDenyTheRest(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	good := filepath.Join(root, "good")
	mkRepo(t, good)

	rep := surveyFleet([]string{filepath.Join(root, "does-not-exist"), good}, "")
	if len(rep.Repos) != 2 {
		t.Fatalf("every path must be reported, got %d", len(rep.Repos))
	}
	if rep.Repos[0].Error == "" {
		t.Error("an unreadable path must record why")
	}
}

// The scan is one level deep on purpose: a recursive walk of a dev directory
// descends into node_modules and vendor trees, which is slow and surfaces repos
// nobody meant to include.
func TestFleetPaths_ScansOneLevelAndDedupes(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	mkRepo(t, a)
	// A nested repo one level deeper must NOT be picked up.
	nested := filepath.Join(a, "vendor", "dep")
	mkRepo(t, nested)

	paths, err := fleetPaths(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want just the top-level repo", paths)
	}

	// An explicit path already found by the scan must not appear twice.
	paths, err = fleetPaths(root, []string{a})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Errorf("paths = %v, want the duplicate collapsed", paths)
	}
}

func TestFleet_UsageErrors(t *testing.T) {
	if code, _, errb := run("fleet"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("bare fleet: code=%d err=%q", code, errb)
	}
	if code, _, errb := run("fleet", "bogus"); code != 2 || !strings.Contains(errb, "usage:") {
		t.Errorf("unknown subcommand: code=%d err=%q", code, errb)
	}
	// No repos named at all is a usage error, not a silent empty pass: an empty
	// rollup reporting "0 bypassed" would read as a clean fleet.
	if code, _, errb := run("fleet", "status"); code != 2 || !strings.Contains(errb, "no repositories") {
		t.Errorf("no paths: code=%d err=%q", code, errb)
	}
}

// The JSON form is what a dashboard consumes, so its shape is part of the
// contract.
func TestFleet_JSONShape(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))

	code, out, errb := run("fleet", "status", "--json", "--root", root)
	if code != 0 {
		t.Fatalf("fleet status --json: code=%d err=%q", code, errb)
	}
	var rep fleetReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Repos) != 1 {
		t.Errorf("repos = %d, want 1", len(rep.Repos))
	}
}

func TestBypassRate(t *testing.T) {
	cases := []struct {
		repo fleetRepo
		want float64
	}{
		{fleetRepo{Commits: 0, Bypassed: 0}, 0}, // no division by zero
		{fleetRepo{Commits: 4, Bypassed: 1}, 25},
		{fleetRepo{Commits: 3, Bypassed: 1}, 33.3}, // rounded, not 33.33333…
		{fleetRepo{Commits: 61, Bypassed: 61}, 100},
	}
	for _, tc := range cases {
		if got := tc.repo.BypassRate(); got != tc.want {
			t.Errorf("%d/%d: rate = %v, want %v", tc.repo.Bypassed, tc.repo.Commits, got, tc.want)
		}
	}
}

// mkRepo creates an initialized git repo with one commit at dir.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		// A non-address identity on purpose: git does not validate this field, and
		// a literal that LOOKS like an email trips secret/PII scanners on a test
		// fixture that contains no address at all.
		{"config", "user.email", "warden-fleet-test"},
		{"config", "user.name", "warden-fleet-test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := gitAt(dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// gitAt runs git in dir, returning combined output.
func gitAt(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// printFleet is the human surface, and every branch in it is a different verdict
// a reader acts on differently — so each one is rendered here.
func TestPrintFleet_RendersEveryVerdict(t *testing.T) {
	rep := fleetReport{
		Adopted: 3, Skipped: 2, Stalled: 1,
		Commits: 10, Verified: 5, Bypassed: 3, Reattestable: 2, BypassRate: 30,
		Repos: []fleetRepo{
			{Path: "/x/bypassed", Adopted: true, Commits: 4, Verified: 1, Bypassed: 3},
			{Path: "/x/recoverable", Adopted: true, Commits: 4, Verified: 2, Reattestable: 2},
			{Path: "/x/clean", Adopted: true, Commits: 2, Verified: 2},
			{Path: "/x/stalled", Configured: true},
			{Path: "/x/unrelated"},
		},
	}
	var b strings.Builder
	printFleet(&b, rep)
	out := b.String()

	for _, want := range []string{
		"3 repos gated",
		"1 configured but never adopted",
		"30.0%",             // the headline rate
		"bypassed",          // the failing repo
		"reattestable",      // the recoverable one
		"clean",             // the passing one
		"run `warden init`", // the actionable stalled hint
		"not warden-gated",  // the irrelevant one
		"protects nothing",  // why a bypass matters
		"are NOT bypasses",  // why a reattestable gap does not
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A clean fleet must not print the two explanatory footers — advice nobody needs
// is noise, and noise is what makes people stop reading a report.
func TestPrintFleet_QuietWhenNothingIsWrong(t *testing.T) {
	var b strings.Builder
	printFleet(&b, fleetReport{
		Adopted: 1, Commits: 3, Verified: 3,
		Repos: []fleetRepo{{Path: "/x/clean", Adopted: true, Commits: 3, Verified: 3}},
	})
	out := b.String()
	if strings.Contains(out, "protects nothing") || strings.Contains(out, "NOT bypasses") {
		t.Errorf("a clean fleet should print no remediation footers:\n%s", out)
	}
}

// An empty rollup must not render a commit line claiming a clean sweep of zero.
func TestPrintFleet_NoCommitsPrintsNoRate(t *testing.T) {
	var b strings.Builder
	printFleet(&b, fleetReport{Skipped: 1, Repos: []fleetRepo{{Path: "/x/none"}}})
	if strings.Contains(b.String(), "commits since adoption") {
		t.Errorf("no commits should not render a rate line:\n%s", b.String())
	}
}

// A genuinely gated repo exercises the counting path end to end, and a bypassed
// commit must make the command exit non-zero so it composes as a CI check.
func TestFleet_ExitsNonZeroOnARealBypass(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	writeConfig(t, dir, "hooks: { pre_push: true }\nsteps: { pre_push: [lint] }\ncommands: { lint: \"true\" }\nrules: []\n")
	if code, _, errb := run("init"); code != 0 {
		t.Fatalf("init: %d %q", code, errb)
	}
	// A commit made without the gate is a bypass by construction.
	if out, err := gitAt(dir, "commit", "--allow-empty", "--no-verify", "-m", "snuck past"); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}

	rep := surveyFleet([]string{dir}, "")
	if rep.Adopted != 1 {
		t.Fatalf("the repo should be adopted: %+v", rep.Repos)
	}
	code, _, _ := run("fleet", "status", dir)
	if rep.Bypassed > 0 && code == 0 {
		t.Error("a bypassed commit must make fleet status exit non-zero")
	}
}
