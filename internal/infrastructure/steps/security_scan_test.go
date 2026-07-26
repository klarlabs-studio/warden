package steps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// fakeNox is a stand-in for the scanner. It copies whatever nox-report.json the
// tree under scan carries into the report directory it was handed, and exits
// with the code in nox-exit. That makes the *tree* decide what the scan finds,
// which is exactly what the delta path needs: the base commit and HEAD can
// carry different reports, and warden's job is to tell them apart.
const fakeNox = `#!/bin/sh
if [ "$1" = "version" ]; then echo "nox ${FAKE_NOX_VERSION:-1.15.0} (commit: test)"; exit 0; fi
[ -n "$FAKE_NOX_LOG" ] && echo "scan" >> "$FAKE_NOX_LOG"
out="."
prev=""
for a in "$@"; do
  if [ "$prev" = "-output" ]; then out="$a"; fi
  prev="$a"
done
mkdir -p "$out"
if [ -f nox-report.json ]; then
  cp nox-report.json "$out/findings.json"
else
  printf '{"meta":{"tool_version":"1.15.0"},"findings":[]}' > "$out/findings.json"
fi
if [ -f nox-exit ]; then exit "$(cat nox-exit)"; fi
exit 0
`

// installFakeNox puts the fake scanner first on PATH for the test.
func installFakeNox(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake scanner is a POSIX shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nox"), []byte(fakeNox), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// report renders a findings.json body with one entry per fingerprint.
func report(findings ...string) string {
	var b strings.Builder
	b.WriteString(`{"meta":{"tool_version":"1.15.0"},"findings":[`)
	for i, fp := range findings {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"RuleID":"SEC-001","Severity":"high","Fingerprint":"` + fp +
			`","Message":"secret detected","Location":{"FilePath":"main.go","StartLine":1}}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// repoWithBaseAndHead builds a repo whose upstream commit reports baseReport and
// whose HEAD reports headReport, so a delta scan has two different trees to
// compare.
func repoWithBaseAndHead(t *testing.T, baseReport, headReport string) string {
	t.Helper()
	dir := newGitRepo(t)
	writeFile(t, dir, "nox-report.json", baseReport)
	writeFile(t, dir, "nox-exit", "1")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "base state")

	runGit(t, dir, "checkout", "-q", "-b", "feature")
	runGit(t, dir, "branch", "--set-upstream-to=main", "feature")
	writeFile(t, dir, "nox-report.json", headReport)
	// The change under review always touches something, even when it changes no
	// findings — that is the case delta gating exists for.
	writeFile(t, dir, "change.txt", "the unrelated one-line change\n")
	commitAll(t, dir, "the change under review")
	return dir
}

// commitAll stages the worktree and commits it, tolerating a no-op change.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

func scanContext(dir string, cfg domain.SecurityScanConfig) application.StepContext {
	return application.StepContext{
		WorktreeDir:  dir,
		Commands:     map[string]string{"security-scan": "nox scan . -severity-threshold high"},
		SecurityScan: cfg,
	}
}

func runScan(t *testing.T, sc application.StepContext) domain.StepResult {
	t.Helper()
	res, err := NewSecurityScanStep(domain.StepSecurityScan).Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func findingsText(res domain.StepResult) string {
	var b strings.Builder
	for _, f := range res.Findings {
		b.WriteString(f.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func TestSecurityScanStep_DeltaGating(t *testing.T) {
	installFakeNox(t)

	t.Run("a finding the change introduced fails the push", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report("old"), report("old", "new"))
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))

		if res.Status != domain.StepFail {
			t.Fatalf("status = %s, want fail; summary=%q", res.Status, res.Summary)
		}
		if !strings.Contains(res.Summary, "1 new finding") {
			t.Errorf("summary = %q, want it to count exactly the introduced finding", res.Summary)
		}
		if !strings.Contains(res.Summary, "1 pre-existing not counted") {
			t.Errorf("summary = %q, want the inherited finding reported as not counted", res.Summary)
		}
		blocking := 0
		for _, f := range res.Findings {
			if f.Severity == domain.SeverityHigh {
				blocking++
			}
		}
		if blocking != 1 {
			t.Errorf("got %d blocking findings, want 1: only what the change added should block", blocking)
		}
	})

	t.Run("an inherited backlog warns instead of blocking", func(t *testing.T) {
		// This is the bug: a YAML-only commit blocked by 71 pre-existing
		// findings it never touched, which gets the push retried with
		// --no-verify and removes the gate entirely.
		dir := repoWithBaseAndHead(t, report("old1", "old2"), report("old1", "old2"))
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))

		if res.Status != domain.StepPass {
			t.Fatalf("status = %s, want pass; summary=%q findings=%s", res.Status, res.Summary, findingsText(res))
		}
		if !strings.Contains(res.Summary, "2 pre-existing") {
			t.Errorf("summary = %q, want the inherited count reported", res.Summary)
		}
		if !strings.Contains(findingsText(res), "do not block the push") {
			t.Errorf("findings = %q, want an explicit advisory about the backlog", findingsText(res))
		}
		// A high-severity finding makes the push gate demand human approval, so
		// a warning at that severity would rebuild the wall it replaces.
		for _, f := range res.Findings {
			if f.Severity == domain.SeverityHigh {
				t.Errorf("the pre-existing warning is high severity (%q); it would trigger the approval gate", f.Message)
			}
		}
	})

	t.Run("a clean tree passes", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report(), report())
		writeFile(t, dir, "nox-exit", "0")
		commitAll(t, dir, "clean")

		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))
		if res.Status != domain.StepPass {
			t.Fatalf("status = %s, want pass; findings=%s", res.Status, findingsText(res))
		}
	})

	t.Run("total mode still gates on the whole tree", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report("old1", "old2"), report("old1", "old2"))
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{Mode: domain.ScanModeTotal}))

		if res.Status != domain.StepFail {
			t.Fatalf("status = %s, want fail: total mode is the opt-in strict gate", res.Status)
		}
		if !strings.Contains(res.Summary, "2 findings") {
			t.Errorf("summary = %q, want all findings counted", res.Summary)
		}
	})

	t.Run("a configured base ref is honored", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report("old"), report("old", "new"))
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{Base: "main"}))
		if res.Status != domain.StepFail || !strings.Contains(res.Summary, "1 new finding") {
			t.Errorf("result = %+v, want the delta measured against the configured base", res)
		}
	})

	t.Run("an unresolvable configured base fails closed", func(t *testing.T) {
		// Warden must never silently widen the gate because it could not work
		// out what to compare against.
		dir := repoWithBaseAndHead(t, report("old"), report("old", "new"))
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{Base: "refs/heads/nope"}))
		if res.Status != domain.StepFail {
			t.Fatalf("status = %s, want fail", res.Status)
		}
		if !strings.Contains(findingsText(res), "could not determine a base commit") {
			t.Errorf("findings = %q, want the reason spelled out", findingsText(res))
		}
	})
}

func TestSecurityScanStep_BaseScanIsCached(t *testing.T) {
	installFakeNox(t)
	dir := repoWithBaseAndHead(t, report("old"), report("old", "new"))
	log := filepath.Join(t.TempDir(), "scans.log")
	t.Setenv("FAKE_NOX_LOG", log)

	sc := scanContext(dir, domain.SecurityScanConfig{})
	runScan(t, sc)
	first := scanCount(t, log)
	runScan(t, sc)
	second := scanCount(t, log)

	// A repo with a standing backlog fails the gate on every push, so re-scanning
	// the unchanged base commit each time would double the cost precisely for the
	// repos delta gating is meant to unblock.
	if first != 2 {
		t.Fatalf("first run made %d scans, want 2 (HEAD + base)", first)
	}
	if second-first != 1 {
		t.Errorf("second run made %d scans, want 1 (HEAD only; the base is unchanged)", second-first)
	}
}

func scanCount(t *testing.T, log string) int {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // test-owned temp path
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "scan\n")
}

func TestSecurityScanStep_VersionDrift(t *testing.T) {
	installFakeNox(t)

	pin := func(t *testing.T, dir, version string) {
		t.Helper()
		writeFile(t, dir, ".github/workflows/ci.yml", "env:\n  NOX_VERSION: \""+version+"\"\n")
	}

	// A version difference ALONE is not harm. This check used to refuse on it
	// before scanning, and the proxy was wrong far more often than right:
	// measured on one real repo, nox 1.17.0, 1.20.0 and 1.22.0 all produced
	// "0 findings, 969 suppressed" against the same committed baseline. Every
	// push was refused for a harm that was not occurring, and the only ways past
	// were hand-installing a binary or bumping a pin that drifts again within
	// hours (brew auto-upgrades it). A gate you cannot satisfy is a gate people
	// escape with --no-verify, which also disables the tests and the scan.
	t.Run("a version difference the baseline survived is allowed, with a note", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report("kept"), report("kept"))
		writeFile(t, dir, "nox-exit", "0")
		// The baseline still matches what the scan reports — the fingerprints did
		// not move between these versions, so nothing is broken.
		writeFile(t, dir, ".nox/baseline.json",
			`{"schema_version":"1.0.0","entries":[{"fingerprint":"kept"}]}`)
		pin(t, dir, "1.3.0")
		commitAll(t, dir, "pin")
		t.Setenv("FAKE_NOX_VERSION", "1.15.0")

		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))
		if res.Status != domain.StepFail && res.Status != domain.StepPass {
			t.Fatalf("unexpected status %s", res.Status)
		}
		if res.Status == domain.StepFail {
			t.Fatalf("status = fail, want the push allowed: the baseline still matches, so the "+
				"version difference did no harm; findings=%s", findingsText(res))
		}
		msg := findingsText(res)
		if !strings.Contains(msg, "1.15.0") || !strings.Contains(msg, "1.3.0") {
			t.Errorf("message = %q, want both versions still named — allowed is not the same as unmentioned", msg)
		}
	})

	// The case the pin exists for: the versions differ AND the baseline has
	// stopped matching. Now it can be asserted as fact rather than suspected.
	t.Run("refuses when the version difference has broken the baseline", func(t *testing.T) {
		// The report must be non-empty and share nothing with the baseline: that
		// total miss is the evidence the rule ids were renumbered.
		dir := repoWithBaseAndHead(t, report("renamed"), report("renamed"))
		writeFile(t, dir, ".nox/baseline.json",
			`{"schema_version":"1.0.0","entries":[{"fingerprint":"gone1"},{"fingerprint":"gone2"}]}`)
		pin(t, dir, "1.3.0")
		t.Setenv("FAKE_NOX_VERSION", "1.15.0")

		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))
		if res.Status != domain.StepFail {
			t.Fatalf("status = %s, want fail: a renumbered ruleset invalidates every baseline entry", res.Status)
		}
		msg := findingsText(res)
		// Naming both versions is the whole value: the fix is obvious once you
		// can see which two numbers disagree.
		if !strings.Contains(msg, "1.15.0") || !strings.Contains(msg, "1.3.0") {
			t.Errorf("message = %q, want both the local version and the pin named", msg)
		}
		if !strings.Contains(msg, ".github/workflows/ci.yml") {
			t.Errorf("message = %q, want the file that carries the pin named", msg)
		}
	})

	t.Run("a matching pin scans normally", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report(), report())
		writeFile(t, dir, "nox-exit", "0")
		pin(t, dir, "1.15.0")
		commitAll(t, dir, "pin")
		t.Setenv("FAKE_NOX_VERSION", "1.15.0")

		if res := runScan(t, scanContext(dir, domain.SecurityScanConfig{})); res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass; findings=%s", res.Status, findingsText(res))
		}
	})

	t.Run("no pin means nothing to disagree with", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report(), report())
		writeFile(t, dir, "nox-exit", "0")
		commitAll(t, dir, "clean")
		t.Setenv("FAKE_NOX_VERSION", "9.9.9")

		if res := runScan(t, scanContext(dir, domain.SecurityScanConfig{})); res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass: most repos pin nothing and must not be blocked", res.Status)
		}
	})

	t.Run("version_check: false opts out", func(t *testing.T) {
		dir := repoWithBaseAndHead(t, report(), report())
		writeFile(t, dir, "nox-exit", "0")
		pin(t, dir, "1.3.0")
		commitAll(t, dir, "pin")
		t.Setenv("FAKE_NOX_VERSION", "1.15.0")

		off := false
		res := runScan(t, scanContext(dir, domain.SecurityScanConfig{VersionCheck: &off}))
		if res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass when the check is switched off; findings=%s", res.Status, findingsText(res))
		}
	})
}

func TestSecurityScanStep_BaselineDrift(t *testing.T) {
	installFakeNox(t)
	dir := repoWithBaseAndHead(t, report("old"), report("old", "new"))
	// A baseline whose entries match nothing the scan reported: the signature of
	// a scanner renumbering its rules, not of the tree regressing.
	writeFile(t, dir, ".nox/baseline.json",
		`{"schema_version":"1.0.0","entries":[{"fingerprint":"gone1"},{"fingerprint":"gone2"}]}`)

	res := runScan(t, scanContext(dir, domain.SecurityScanConfig{}))
	msg := findingsText(res)
	if !strings.Contains(msg, "baseline drift") {
		t.Errorf("findings = %q, want the total baseline miss diagnosed as drift", msg)
	}
	// Drift changes the diagnosis, not the verdict: the introduced finding still
	// fails the push.
	if res.Status != domain.StepFail {
		t.Errorf("status = %s, want fail: the introduced finding still blocks", res.Status)
	}
}

func TestSecurityScanStep_FallsBackToExitCodeGating(t *testing.T) {
	installFakeNox(t)

	t.Run("a scanner warden cannot read keeps the old behavior", func(t *testing.T) {
		dir := newGitRepo(t)
		sc := scanContext(dir, domain.SecurityScanConfig{})
		sc.Commands["security-scan"] = "echo audit failed >&2; exit 1"
		if res := runScan(t, sc); res.Status != domain.StepFail {
			t.Errorf("status = %s, want fail: an unrecognized scan command still gates on its exit code", res.Status)
		}

		sc.Commands["security-scan"] = "true"
		if res := runScan(t, sc); res.Status != domain.StepPass {
			t.Errorf("status = %s, want pass", res.Status)
		}
	})

	t.Run("no command configured is an advisory skip", func(t *testing.T) {
		sc := scanContext(t.TempDir(), domain.SecurityScanConfig{})
		sc.Commands = map[string]string{}
		res := runScan(t, sc)
		if res.Status != domain.StepPass || !strings.Contains(res.Summary, "skipped") {
			t.Errorf("result = %+v, want an advisory skip", res)
		}
	})

	t.Run("a recognized scanner that produces no report gates on its exit code", func(t *testing.T) {
		// The scanner crashed, or the step timed out. "I could not check" is not
		// "the tree is clean".
		dir := t.TempDir()
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "nox"), []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		if res := runScan(t, scanContext(dir, domain.SecurityScanConfig{})); res.Status != domain.StepFail {
			t.Errorf("status = %s, want fail", res.Status)
		}
	})
}

func TestSecurityScanStep_Name(t *testing.T) {
	if got := NewSecurityScanStep(domain.StepSecurityScan).Name(); got != domain.StepSecurityScan {
		t.Errorf("Name() = %s, want security-scan", got)
	}
}
