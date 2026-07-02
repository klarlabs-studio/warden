package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"go.klarlabs.de/warden/internal/domain"
)

// editorDoneMsg is delivered after the launched editor exits and the TUI has
// reclaimed the terminal.
type editorDoneMsg struct{}

// openFinding returns a command that suspends the TUI, opens the finding's file
// at its line in $EDITOR (falling back to vi), and resumes. A finding with no
// file, or no editor available, is a no-op. Paths are relative to the repo root
// (the TUI's working directory), where the developer's real files live — not the
// disposable worktree the finding was observed in.
func openFinding(f domain.Finding) tea.Cmd {
	if f.File == "" {
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	name, args := editorCommand(editor, f.File, f.Line)
	if _, err := exec.LookPath(name); err != nil {
		return nil
	}
	return tea.ExecProcess(exec.Command(name, args...), func(error) tea.Msg { return editorDoneMsg{} })
}

// editorCommand builds the argv to open file at line. VS Code-family editors use
// `-g file:line`; the common terminal editors (vi/vim/nano/emacs) take `+line
// file`. Line 0 (unknown) just opens the file.
func editorCommand(editor, file string, line int) (name string, args []string) {
	// $EDITOR may carry flags ("code -w"); the first token is the binary.
	fields := strings.Fields(editor)
	name = fields[0]
	base := fields[1:]

	if isVSCode(name) {
		target := file
		if line > 0 {
			target = fmt.Sprintf("%s:%d", file, line)
		}
		return name, append(base, "-g", target)
	}
	if line > 0 {
		return name, append(base, fmt.Sprintf("+%d", line), file)
	}
	return name, append(base, file)
}

func isVSCode(name string) bool {
	switch name {
	case "code", "code-insiders", "codium", "cursor":
		return true
	default:
		return false
	}
}
