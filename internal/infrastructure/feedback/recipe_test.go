package feedback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docsMatcher extracts the VS Code problem matcher the documentation tells
// users to paste.
func docsMatcher(t *testing.T) (*regexp.Regexp, map[string]int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "background-feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	for block := range strings.SplitSeq(string(data), "```json\n") {
		body, _, ok := strings.Cut(block, "\n```")
		if !ok || !strings.Contains(body, "problemMatcher") {
			continue
		}
		var tasks struct {
			Tasks []struct {
				ProblemMatcher struct {
					Pattern map[string]any `json:"pattern"`
				} `json:"problemMatcher"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(body), &tasks); err != nil {
			t.Fatalf("the documented tasks.json is not valid JSON: %v", err)
		}
		pattern := tasks.Tasks[0].ProblemMatcher.Pattern
		groups := map[string]int{}
		for _, name := range []string{"file", "line", "severity", "code", "message"} {
			n, ok := pattern[name].(float64)
			if !ok {
				t.Fatalf("matcher has no %s group", name)
			}
			groups[name] = int(n)
		}
		// The expression uses only syntax JavaScript and Go read alike.
		return regexp.MustCompile(pattern["regexp"].(string)), groups
	}
	t.Fatal("no VS Code problem matcher in docs/background-feedback.md")
	return nil, nil
}

// The documented matcher must pick up exactly the diagnostic lines of every
// golden problems output, with the right parts in the right groups, and never
// a warden: status line.
func TestDocumentedProblemMatcherReadsTheGoldenOutput(t *testing.T) {
	re, groups := docsMatcher(t)
	paths, err := filepath.Glob(filepath.Join("testdata", "problems", "*.txt"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden problems output: %v", err)
	}
	matched := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
			m := re.FindStringSubmatch(line)
			if strings.HasPrefix(line, "warden: ") {
				if m != nil {
					t.Errorf("%s: the matcher took a status line: %q", path, line)
				}
				continue
			}
			if m == nil {
				t.Errorf("%s: the matcher missed %q", path, line)
				continue
			}
			matched++
			file, lineNo, severity, code, message := m[groups["file"]], m[groups["line"]], m[groups["severity"]], m[groups["code"]], m[groups["message"]]
			if n, err := strconv.Atoi(lineNo); err != nil || n < 1 {
				t.Errorf("%s: line %q", path, lineNo)
			}
			want := file + ":" + lineNo + ": " + severity + ": [" + code + "] " + message
			if want != line || strings.Contains(code, " ") || message == "" {
				t.Errorf("%s: groups %q from %q", path, m[1:], line)
			}
		}
	}
	if matched < 5 {
		t.Fatalf("only %d diagnostic lines in the golden output; the check proves little", matched)
	}
	// The anchor is reported as a configuration anchor, not as a finding.
	m := re.FindStringSubmatch(".warden.yaml:4: error: [test/config-anchor] test failed: test failed (exit 1)")
	if m == nil || m[groups["code"]] != "test/"+ConfigAnchorRule || m[groups["file"]] != ".warden.yaml" {
		t.Fatalf("anchor line: %q", m)
	}
}
