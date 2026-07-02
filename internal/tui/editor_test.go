package tui

import (
	"reflect"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestEditorCommand(t *testing.T) {
	cases := []struct {
		name     string
		editor   string
		file     string
		line     int
		wantName string
		wantArgs []string
	}{
		{"vi with line", "vi", "auth/token.go", 42, "vi", []string{"+42", "auth/token.go"}},
		{"vim no line", "vim", "main.go", 0, "vim", []string{"main.go"}},
		{"vscode uses -g file:line", "code", "a.go", 7, "code", []string{"-g", "a.go:7"}},
		{"vscode no line", "code", "a.go", 0, "code", []string{"-g", "a.go"}},
		{"editor with flags keeps them", "code -w", "a.go", 3, "code", []string{"-w", "-g", "a.go:3"}},
		{"cursor is vscode-family", "cursor", "a.go", 5, "cursor", []string{"-g", "a.go:5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, args := editorCommand(c.editor, c.file, c.line)
			if name != c.wantName || !reflect.DeepEqual(args, c.wantArgs) {
				t.Errorf("editorCommand(%q,%q,%d) = %q %v, want %q %v",
					c.editor, c.file, c.line, name, args, c.wantName, c.wantArgs)
			}
		})
	}
}

func TestOpenFinding_NoFileIsNoOp(t *testing.T) {
	if cmd := openFinding(domain.Finding{Message: "no file"}); cmd != nil {
		t.Error("a finding with no file must not launch an editor")
	}
}
