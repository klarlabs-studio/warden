package feedback

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// feedbackImport is this package's import path.
const feedbackImport = "go.klarlabs.de/warden/internal/infrastructure/feedback"

// readers are the only files outside this package (and the domain's schema)
// allowed to touch the feedback document: the staged watcher that writes it,
// the reader command, and the read-only reader surfaces for agents (the MCP
// feedback_status tool and `warden axi feedback`), each in a file of its own
// so the rest of the MCP server and the axi surface stay checked. Paths are
// relative to the module root.
var readers = map[string]bool{
	"internal/cli/cmd_feedback.go":       true,
	"internal/cli/cmd_watch.go":          true,
	"internal/cli/cmd_watch_feedback.go": true,
	"internal/cli/facade_feedback.go":    true,
	"internal/cli/cmd_axi_feedback.go":   true,
	"internal/mcp/feedback.go":           true,
}

// literalAllowed are the files that may spell the bare word "feedback": the
// command and agent-verb dispatchers, which name the verb and nothing else.
var literalAllowed = map[string]bool{
	"internal/cli/cli.go":     true,
	"internal/cli/cmd_axi.go": true,
}

// The feedback document is unsigned and advisory. It must be impossible for
// anything that decides — the gate, verify, attest, notes, execution
// requirements, the MCP run tools — to read it. This test fails the build if
// any production file other than the writer and the readers imports this
// package, names the document's types, or spells its paths.
func TestNothingThatDecidesReadsTheFeedbackDocument(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/infrastructure/feedback/") || rel == "internal/domain/feedback.go" || readers[rel] {
			return nil
		}
		checked++
		for _, problem := range feedbackReferences(t, path, rel) {
			t.Errorf("%s: %s", rel, problem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d files checked; is the module root right (%s)?", checked, root)
	}
}

func feedbackReferences(t *testing.T, path, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var problems []string
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == feedbackImport {
			problems = append(problems, "imports the feedback package")
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if strings.HasPrefix(n.Name, "Feedback") || strings.HasPrefix(n.Name, "NewFeedback") || strings.HasPrefix(n.Name, "Freshness") {
				problems = append(problems, "names "+n.Name)
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(n.Value)
			switch {
			case value == "status.json", value == "watch.lock",
				strings.Contains(value, "warden.feedback"), strings.Contains(value, "warden/feedback"),
				value == "feedback" && !literalAllowed[rel]:
				problems = append(problems, "spells "+strconv.Quote(value))
			}
		}
		return true
	})
	return problems
}

// The check itself must catch a reference, or it proves nothing.
func TestBoundaryCheckCatchesAReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gate.go")
	src := `package gate

import fb "` + feedbackImport + `"

var _ = fb.StatusPath
var _ = domain.FeedbackDocument{}
var p = "status.json"
`
	if err := writeFile(path, src); err != nil {
		t.Fatal(err)
	}
	if got := feedbackReferences(t, path, "internal/service/gate.go"); len(got) != 3 {
		t.Fatalf("problems found: %v", got)
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }
