package importer

import (
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

const stepHuskyPreCommit domain.StepName = "husky-pre-commit"

// Invoke the tracked entrypoint, not Husky's generated launcher. The launcher
// can exit successfully without executing a hook (missing file or HUSKY=0).
// Match Husky's fail-fast shell and local binary PATH. Explicit bridge execution
// enables the hook even when the host disables Husky's Git launchers.
const huskyPreCommitCommand = `warden_husky_tree=$(git write-tree) &&
HUSKY=1 PATH="node_modules/.bin:$PATH" sh -e .husky/pre-commit &&
git diff --quiet -- && test "$(git write-tree)" = "$warden_husky_tree" || {
  echo 'warden: Husky hook failed or changed tracked files/index; run formatters in your checkout and stage the result before retrying' >&2
  exit 1
}`

func (c *collector) fromHusky(root string) {
	dir := filepath.Join(root, ".husky")
	info, err := os.Lstat(dir)
	if err != nil {
		return
	}
	if !info.IsDir() {
		c.notes = append(c.notes, "husky: .husky is not a regular directory; review its hooks manually")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		c.notes = append(c.notes, "husky: could not read .husky; review its hooks manually")
		return
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "pre-commit":
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				c.notes = append(c.notes, "husky: pre-commit is not a regular file; not imported")
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || strings.TrimSpace(string(data)) == "" {
				c.notes = append(c.notes, "husky: pre-commit is empty or unreadable; not imported")
				continue
			}
			c.setCommand(string(stepHuskyPreCommit), huskyPreCommitCommand,
				"husky: pre-commit <- .husky/pre-commit (isolated writer step; retain the script and its dependencies)")
			c.notes = append(c.notes,
				"husky: bridge requires host Git/index access; track the hook and required helpers before running; host startup files are not loaded and HUSKY=0 does not disable this explicit step",
				"husky: review core.hooksPath after warden init; a Husky hooks path prevents Git from reaching Warden's .git/hooks shims (see docs/hook-ecosystem-bridge.md)")
			if strings.Contains(string(data), "husky.sh") {
				c.notes = append(c.notes, "husky: legacy bootstrap detected; its helper must exist inside the worktree or execution will fail; migrate to a standalone hook when possible")
			}
		case "pre-push", "commit-msg", "prepare-commit-msg", "post-commit", "post-merge", "post-checkout", "pre-rebase", "post-rewrite", "applypatch-msg", "pre-applypatch", "post-applypatch", "pre-merge-commit", "pre-auto-gc", "sendemail-validate", "post-index-change":
			c.notes = append(c.notes, "husky: .husky/"+entry.Name()+" not imported; Git arguments, stdin, and lifecycle semantics require manual configuration")
		}
	}
}
