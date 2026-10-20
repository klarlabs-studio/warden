package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// sarifDoc builds a minimal SARIF 2.1.0 log with the given results.
func sarifDoc(t *testing.T, results ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool":    map[string]any{"driver": map[string]any{"name": "fixture", "rules": []any{map[string]any{"id": "R2", "defaultConfiguration": map[string]any{"level": "note"}}}}},
			"results": results,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sarifResultAt(rule, level, uri string, line int, msg string) map[string]any {
	r := map[string]any{"ruleId": rule, "message": map[string]any{"text": msg}}
	if level != "" {
		r["level"] = level
	}
	if uri != "" {
		loc := map[string]any{"artifactLocation": map[string]any{"uri": uri}}
		if line > 0 {
			loc["region"] = map[string]any{"startLine": line}
		}
		r["locations"] = []any{map[string]any{"physicalLocation": loc}}
	}
	return r
}

// failWriting returns a shell command that writes data to rel and exits 1.
func failWriting(t *testing.T, rel string, data []byte) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "fixture.sarif")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return "mkdir -p \"$(dirname " + rel + ")\" && cp " + shellQuote(src) + " " + rel + " && echo 'lint: 2 problems' && exit 1"
}

func runLint(t *testing.T, dir, command, diagnostics string) (domain.StepResult, []string) {
	t.Helper()
	var lines []string
	sc := application.StepContext{
		WorktreeDir: dir,
		Commands:    map[string]string{"lint": command},
		Diagnostics: diagnostics,
		OnOutput:    func(l string) { lines = append(lines, l) },
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	return res, lines
}

func sarifFindings(res domain.StepResult) []domain.Finding {
	var out []domain.Finding
	for _, f := range res.Findings {
		if f.FromSARIF {
			out = append(out, f)
		}
	}
	return out
}

func TestShellStep_FailingCheckIngestsDeclaredSARIF(t *testing.T) {
	dir := t.TempDir()
	data := sarifDoc(t,
		sarifResultAt("E1", "error", "file:///workspace/cmd/main.go", 12, "unchecked error"),
		sarifResultAt("W1", "warning", filepath.ToSlash(dir)+"/pkg/a.go", 3, "shadowed"),
		sarifResultAt("R2", "", "internal/b.go", 7, "uses rule default"),
		sarifResultAt("X", "error", "/etc/passwd", 1, "outside"),
		sarifResultAt("X", "error", "../escape.go", 1, "climbs out"),
		sarifResultAt("X", "error", "https://example.com/x.go", 1, "another scheme"),
		sarifResultAt("N", "error", "", 0, "no location"),
	)
	res, lines := runLint(t, dir, failWriting(t, "out/lint.sarif", data), "out/lint.sarif")

	if res.Status != domain.StepFail {
		t.Fatalf("status %s", res.Status)
	}
	if res.Findings[0].FromSARIF || !strings.Contains(res.Findings[0].Message, "lint: 2 problems") {
		t.Fatalf("the verbatim output must stay the first finding: %+v", res.Findings[0])
	}
	got := sarifFindings(res)
	want := []domain.Finding{
		{Severity: domain.SeverityHigh, Rule: "E1", Message: "unchecked error", File: "cmd/main.go", Line: 12, FromSARIF: true},
		{Severity: domain.SeverityMedium, Rule: "W1", Message: "shadowed", File: "pkg/a.go", Line: 3, FromSARIF: true},
		{Severity: domain.SeverityLow, Rule: "R2", Message: "uses rule default", File: "internal/b.go", Line: 7, FromSARIF: true},
		{Severity: domain.SeverityHigh, Rule: "N", Message: "no location", FromSARIF: true},
	}
	if len(got) != len(want) {
		t.Fatalf("findings %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	wantNote := "diagnostics: 4 findings from out/lint.sarif; 3 results outside the repository dropped"
	if !strings.HasSuffix(res.Summary, "; "+wantNote) {
		t.Errorf("summary %q", res.Summary)
	}
	if !containsLine(lines, "warden: "+wantNote) {
		t.Errorf("output %q", lines)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// A pass is never read: the exit status is the only verdict, and a pass with
// no findings stays eligible for a staged prediction.
func TestShellStep_PassingCheckIgnoresSARIFErrors(t *testing.T) {
	dir := t.TempDir()
	data := sarifDoc(t, sarifResultAt("E1", "error", "main.go", 1, "boom"))
	src := filepath.Join(t.TempDir(), "f.sarif")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, lines := runLint(t, dir, "cp "+shellQuote(src)+" lint.sarif", "lint.sarif")
	if res.Status != domain.StepPass || len(res.Findings) != 0 || strings.Contains(res.Summary, "diagnostics") || len(lines) != 0 {
		t.Fatalf("a pass must be untouched: %+v %q", res, lines)
	}
}

func TestShellStep_UndeclaredSARIFIsNotRead(t *testing.T) {
	dir := t.TempDir()
	res, _ := runLint(t, dir, failWriting(t, "lint.sarif", sarifDoc(t, sarifResultAt("E", "error", "a.go", 1, "x"))), "")
	if len(sarifFindings(res)) != 0 || strings.Contains(res.Summary, "diagnostics") {
		t.Fatalf("read a file nobody declared: %+v", res)
	}
}

func TestShellStep_IgnoredSARIFIsANoteNotAFailure(t *testing.T) {
	valid := sarifDoc(t, sarifResultAt("E1", "error", "a.go", 1, "x"))
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string) string // returns the command
		want    string
	}{
		{"not written", func(t *testing.T, dir string) string { return "exit 1" }, "ignored lint.sarif: the check did not write it"},
		{"malformed", func(t *testing.T, dir string) string {
			return failWriting(t, "lint.sarif", []byte("{not json"))
		}, "ignored lint.sarif: not valid SARIF JSON"},
		{"wrong version", func(t *testing.T, dir string) string {
			return failWriting(t, "lint.sarif", []byte(`{"version":"2.0.0","runs":[]}`))
		}, `ignored lint.sarif: SARIF version "2.0.0", want "2.1.0"`},
		{"oversized", func(t *testing.T, dir string) string {
			big := append(append([]byte(`{"version":"2.1.0","runs":[],"pad":"`), make([]byte, sarifMaxBytes)...), '"', '}')
			for i := 36; i < len(big)-2; i++ {
				big[i] = 'a'
			}
			return failWriting(t, "lint.sarif", big)
		}, "ignored lint.sarif: larger than 4 MiB"},
		{"a directory", func(t *testing.T, dir string) string { return "mkdir lint.sarif; exit 1" }, "ignored lint.sarif: not a regular file"},
		{"predates the check", func(t *testing.T, dir string) string {
			// A report committed to the tree that this run never rewrote is
			// not this run's result.
			if err := os.WriteFile(filepath.Join(dir, "lint.sarif"), valid, 0o600); err != nil {
				t.Fatal(err)
			}
			return "exit 1"
		}, "ignored lint.sarif: not rewritten by this run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			res, _ := runLint(t, dir, tc.prepare(t, dir), "lint.sarif")
			if res.Status != domain.StepFail || res.Blocker != domain.BlockerNone {
				t.Fatalf("the verdict must be the command's: %+v", res)
			}
			if len(sarifFindings(res)) != 0 || len(res.Findings) != 1 {
				t.Fatalf("findings %+v", res.Findings)
			}
			if !strings.Contains(res.Summary, "; diagnostics: "+tc.want) {
				t.Fatalf("summary %q, want note %q", res.Summary, tc.want)
			}
		})
	}
}

func TestShellStep_RewrittenPreexistingSARIFIsRead(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "lint.sarif")
	if err := os.WriteFile(old, sarifDoc(t), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	res, _ := runLint(t, dir, failWriting(t, "lint.sarif", sarifDoc(t, sarifResultAt("E", "error", "a.go", 2, "new"))), "lint.sarif")
	if got := sarifFindings(res); len(got) != 1 || got[0].File != "a.go" {
		t.Fatalf("a report the check rewrote must be read: %+v (%s)", got, res.Summary)
	}
}

func TestShellStep_SymlinkedSARIFIsIgnored(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.sarif")
	if err := os.WriteFile(outside, sarifDoc(t, sarifResultAt("LEAK", "error", "a.go", 1, "outside content")), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, declared, want string
		link                 func(t *testing.T, dir string)
	}{
		{"leaf", "lint.sarif", "ignored lint.sarif: lint.sarif is a symlink", func(t *testing.T, dir string) {
			symlinkOrSkip(t, outside, filepath.Join(dir, "lint.sarif"))
		}},
		{"leaf to a file inside", "lint.sarif", "ignored lint.sarif: lint.sarif is a symlink", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "real.sarif"), sarifDoc(t), 0o600); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, "real.sarif", filepath.Join(dir, "lint.sarif"))
		}},
		{"directory component", "reports/out/lint.sarif", "ignored reports/out/lint.sarif: reports is a symlink", func(t *testing.T, dir string) {
			target := filepath.Dir(outside)
			if err := os.MkdirAll(filepath.Join(target, "out"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "out", "lint.sarif"), sarifDoc(t, sarifResultAt("LEAK", "error", "a.go", 1, "x")), 0o600); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, target, filepath.Join(dir, "reports"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.link(t, dir)
			// touch -h moves the link's own mtime, as a check that created
			// it during the run would.
			res, _ := runLint(t, dir, "touch -h "+tc.declared+" 2>/dev/null; exit 1", tc.declared)
			if len(sarifFindings(res)) != 0 {
				t.Fatalf("read through a symlink: %+v", res.Findings)
			}
			if !strings.Contains(res.Summary, "; diagnostics: "+tc.want) {
				t.Fatalf("summary %q, want %q", res.Summary, tc.want)
			}
		})
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// A step the machine stopped never ran its command, so there is no report.
func TestShellStep_BlockedStepReadsNoSARIF(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lint.sarif"), sarifDoc(t, sarifResultAt("E", "error", "a.go", 1, "x")), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := application.StepContext{WorktreeDir: dir, Diagnostics: "lint.sarif"}
	blocked := domain.StepResult{Step: domain.StepLint, Status: domain.StepFail, Blocker: domain.BlockerEnvironment, Summary: "lint could not run"}
	NewShellStep(domain.StepLint, "lint").addDiagnostics(sc, &blocked, diagnosticsStamp{})
	if len(blocked.Findings) != 0 || blocked.Summary != "lint could not run" {
		t.Fatalf("a blocked step must not gain diagnostics: %+v", blocked)
	}
}

func TestNormaliseSARIFURI(t *testing.T) {
	roots := []string{"/workspace/", "/tmp/wt/", "C:/wt/"}
	cases := []struct {
		raw, base, want string
		inside          bool
	}{
		{"cmd/main.go", "", "cmd/main.go", true},
		{"./cmd/main.go", "", "cmd/main.go", true},
		{"cmd/a%20b.go", "", "cmd/a b.go", true},
		{"/workspace/cmd/main.go", "", "cmd/main.go", true},
		{"file:///workspace/cmd/main.go", "", "cmd/main.go", true},
		{"file://localhost/workspace/x.go", "", "x.go", true},
		{"/tmp/wt/pkg/x.go", "", "pkg/x.go", true},
		{"file:///C:/wt/pkg/x.go", "", "pkg/x.go", true},
		{"main.go", "file:///workspace/", "main.go", true},
		{"main.go", "file:///workspace/sub/", "sub/main.go", true},
		{"main.go", "file:///elsewhere/", "", false},
		{"/workspace", "", "", false},
		{"/workspace/", "", "", false},
		{"/workspacex/a.go", "", "", false},
		{"/etc/passwd", "", "", false},
		{"../x.go", "", "", false},
		{"/workspace/../etc/passwd", "", "", false},
		{"file://otherhost/workspace/x.go", "", "", false},
		{"https://example.com/x.go", "", "", false},
		{"file:relative.go", "", "relative.go", true},
	}
	for _, tc := range cases {
		got, inside := normaliseSARIFURI(tc.raw, tc.base, roots)
		if got != tc.want || inside != tc.inside {
			t.Errorf("normalise(%q, base %q) = %q, %v; want %q, %v", tc.raw, tc.base, got, inside, tc.want, tc.inside)
		}
	}
}

func TestSARIFResultFiltering(t *testing.T) {
	doc := `{"version":"2.1.0","runs":[{
	  "tool":{"driver":{"rules":[{"id":"A","defaultConfiguration":{"level":"error"}},{"id":"B"}]}},
	  "originalUriBaseIds":{"ROOT":{"uri":"file:///workspace/"},"SRC":{"uri":"src/","uriBaseId":"ROOT"}},
	  "results":[
	    {"ruleIndex":0,"message":{"text":"by index"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go","uriBaseId":"SRC"},"region":{"startLine":4}}}]},
	    {"rule":{"id":"B"},"message":{"text":"default warning"}},
	    {"ruleId":"C","kind":"pass","message":{"text":"a pass"}},
	    {"ruleId":"D","message":{"text":"suppressed"},"suppressions":[{"kind":"inSource"}]},
	    {"ruleId":"E","level":"note","message":{"text":"rejected suppression"},"suppressions":[{"kind":"external","status":"rejected"}]},
	    {"ruleId":"F","level":"none","message":{}}
	  ]}]}`
	var log sarifLog
	if err := json.Unmarshal([]byte(doc), &log); err != nil {
		t.Fatal(err)
	}
	got, stats := log.findings([]string{"/workspace/"})
	want := []domain.Finding{
		{Severity: domain.SeverityHigh, Rule: "A", Message: "by index", File: "src/a.go", Line: 4, FromSARIF: true},
		{Severity: domain.SeverityMedium, Rule: "B", Message: "default warning", FromSARIF: true},
		{Severity: domain.SeverityLow, Rule: "E", Message: "rejected suppression", FromSARIF: true},
		{Severity: domain.SeverityInfo, Rule: "F", Message: "F", FromSARIF: true},
	}
	if len(got) != len(want) || stats != (sarifStats{}) {
		t.Fatalf("findings %+v stats %+v", got, stats)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSARIFResultCap(t *testing.T) {
	results := make([]map[string]any, sarifMaxResults+5)
	for i := range results {
		results[i] = sarifResultAt("R", "error", "a.go", i+1, "x")
	}
	dir := t.TempDir()
	res, _ := runLint(t, dir, failWriting(t, "lint.sarif", sarifDoc(t, results...)), "lint.sarif")
	if n := len(sarifFindings(res)); n != sarifMaxResults {
		t.Fatalf("%d findings, want the cap %d", n, sarifMaxResults)
	}
	if !strings.Contains(res.Summary, "5 more results not shown (limit 1000)") {
		t.Fatalf("summary %q", res.Summary)
	}
}

// The file is read only through real directories inside the worktree; a
// declared path that escapes is refused even if validation were bypassed.
func TestReadConfinedRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"../x.sarif", "/etc/passwd", "", "a/../../x"} {
		if _, err := readConfined(dir, rel, diagnosticsStamp{}); err == nil {
			t.Errorf("readConfined(%q) succeeded", rel)
		}
	}
}
