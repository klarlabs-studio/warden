package steps

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A command whose executable does not exist has not judged the tree either —
// the same category of non-answer as lock contention (see contention.go), with a
// different remedy. `sh: astro: command not found` means the checkout has no
// node_modules, or the toolchain was never installed; reporting it as "step
// js-build failed" sends the developer reading a build log for a bug that is not
// there, and blocks a push whose diff may not touch that language at all.
//
// Unlike contention this is NOT retryable: the same command will fail the same
// way until a human runs the install. So the two get different exit codes.

// shellMissingRe matches a POSIX shell's own report that it could not find an
// executable. Variants covered:
//
//	sh: astro: command not found            (bash-as-sh, macOS)
//	bash: line 1: astro: command not found
//	sh: 1: astro: not found                 (dash)
//	/bin/sh: astro: not found               (busybox)
//
// Three things keep this from firing on a program's own output. The line must
// START with the shell's argv[0] — `[\w./-]*sh: ` admits "sh: ", "bash: ",
// "/bin/sh: " but not an indented or quoted "  got: sh: …" — the diagnostic must
// END the line, and the tool name is taken from the LAST colon-separated field
// before it, so the shell's own "line 1:" interjection is skipped rather than
// mistaken for the tool.
var shellMissingRe = regexp.MustCompile(`(?m)^[\w./-]*sh: (?:.*: )?([^:\n ][^:\n]*): (?:command )?not found\s*$`)

// zshMissingRe matches zsh, which words the same diagnostic the other way round
// ("zsh: command not found: astro").
var zshMissingRe = regexp.MustCompile(`(?m)^[\w./-]*zsh: command not found: (\S+)\s*$`)

// moduleMissingSignatures mark a JS runtime that started but could not resolve a
// dependency — the same missing-node_modules cause reaching us one layer later,
// when the binary exists (or is invoked via a runner) but the tree does not.
var moduleMissingSignatures = []string{
	"cannot find module",
	"err_module_not_found",
	// npm refusing to run a binary that is not installed.
	"npm err! could not determine executable to run",
}

// pnpmMissingBinRe is pnpm's wording for the same thing — it names the binary
// rather than the module (`Command "astro" not found`), so it needs a pattern
// rather than a fixed substring.
var pnpmMissingBinRe = regexp.MustCompile(`(?i)command "[^"]+" not found`)

// envFailure describes a step that could not start because its tooling is
// absent. Tool is the executable the shell could not find ("" when the signal
// was a module resolution error instead), and Remediation is the exact command
// that would fix it, or "" when warden cannot tell.
type envFailure struct {
	Tool        string
	Remediation string
}

// detectEnvFailure reports whether output shows the command never ran for want
// of its toolchain, and what to do about it. worktreeDir is the directory the
// command ran in; it is inspected (read-only) to name the right install command.
//
// It is deliberately conservative in the same way isContention is: a false
// positive would relabel a genuine build failure as an environment problem and
// tell the developer to run an install that will not help.
func detectEnvFailure(output, worktreeDir string) (envFailure, bool) {
	tool, ok := missingTool(output)
	if !ok && !hasModuleMissing(output) {
		return envFailure{}, false
	}
	return envFailure{Tool: tool, Remediation: installHint(worktreeDir, tool)}, true
}

// missingTool extracts the name of the executable a shell reported missing.
func missingTool(output string) (string, bool) {
	for _, re := range []*regexp.Regexp{shellMissingRe, zshMissingRe} {
		if m := re.FindStringSubmatch(output); m != nil {
			return strings.TrimSpace(m[1]), true
		}
	}
	return "", false
}

// hasModuleMissing reports whether output shows a runtime failing to resolve a
// dependency rather than a shell failing to find a binary.
func hasModuleMissing(output string) bool {
	lower := strings.ToLower(output)
	for _, sig := range moduleMissingSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return pnpmMissingBinRe.MatchString(output)
}

// nodeInstallers maps a lockfile to the install command that reproduces it
// exactly. Order matters: a repo may carry more than one lockfile, and the more
// specific package managers are checked before npm's.
var nodeInstallers = []struct{ lockfile, command string }{
	{"pnpm-lock.yaml", "pnpm install --frozen-lockfile"},
	{"yarn.lock", "yarn install --immutable"},
	{"bun.lockb", "bun install --frozen-lockfile"},
	{"package-lock.json", "npm ci"},
}

// installHint returns the command that would make tool available in dir, or ""
// when warden cannot say with confidence. Guessing wrong is worse than staying
// quiet: an install command that does not apply wastes the reader's time and
// undermines the rest of the message.
//
// The JS case is the one worth resolving precisely, because it is the one that
// bites (a git worktree holds only tracked files, so gitignored node_modules is
// absent unless warden linked it in). The install command is derived from the
// lockfile actually present, so the reader can paste it.
func installHint(dir, tool string) string {
	if dir == "" {
		return ""
	}
	// Walk from the deepest directory a `cd`-scoped command would have run in
	// back to the worktree root, so a monorepo's per-package install is named.
	pkgDir, ok := nearestNodePackage(dir)
	if !ok {
		return ""
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "node_modules")); err == nil {
		// Dependencies ARE installed, so a reinstall is not obviously the fix —
		// the tool is genuinely missing from the environment. Say nothing rather
		// than send the reader down the wrong path.
		return ""
	}
	cmd := "npm install"
	for _, in := range nodeInstallers {
		if _, err := os.Stat(filepath.Join(pkgDir, in.lockfile)); err == nil {
			cmd = in.command
			break
		}
	}
	if rel, err := filepath.Rel(dir, pkgDir); err == nil && rel != "." {
		return cmd + " (in " + rel + ")"
	}
	return cmd
}

// nearestNodePackage finds the package.json nearest to dir, searching dir
// itself and then one level of subdirectories. A warden step for a monorepo
// package is usually written as `cd web && npm run build`, which runs with
// WorktreeDir at the repo root, so the package.json that matters may be a child
// rather than dir itself. The search stops at one level: deeper guessing risks
// naming a package that has nothing to do with the failing step.
func nearestNodePackage(dir string) (string, bool) {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return dir, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(child, "package.json")); err == nil {
			return child, true
		}
	}
	return "", false
}

// message renders the developer-facing explanation for an environment failure.
// It leads with the fact that nothing is wrong with the change, because that is
// the question the reader is actually asking when a push is blocked.
func (e envFailure) message(step string) string {
	var b strings.Builder
	b.WriteString(step + " could not run: ")
	if e.Tool != "" {
		b.WriteString(e.Tool + " is not installed in the validation worktree")
	} else {
		b.WriteString("its dependencies are not installed in the validation worktree")
	}
	b.WriteString(". This is an environment problem, not a problem with your change.")
	if e.Remediation != "" {
		b.WriteString("\nRun: " + e.Remediation)
		b.WriteString("\nIf the install needs a private registry, export NODE_AUTH_TOKEN in your " +
			"shell — do NOT run `npm config set …_authToken`, which writes a live token into " +
			".npmrc, a file most repos track.")
	}
	return b.String()
}
