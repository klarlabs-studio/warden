package steps

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// A step whose command does not exist has not judged the tree — it never ran.
// Reporting `sh: astro: command not found` as "step js-build failed" reads as a
// broken build, so the developer goes looking at their code; the actual problem
// is that nothing installed the toolchain. Worse, chasing it leads straight to
// the credential hazard in SecretsStep's docs.
//
// This mirrors the contention classifier: same "could not run" framing, a
// different cause. Both keep the gate honest by refusing to call an environment
// problem a code problem.
type envFailure struct {
	Summary string
	Message string
}

// notFoundRes capture the command name from the shells' several phrasings:
//
//	zsh: command not found: astro
//	sh: astro: command not found        (dash/ash)
//	bash: line 1: astro: command not found
//	sh: 1: astro: not found
//
// Order matters and they are tried separately rather than joined by `|`: RE2
// returns the LEFTMOST match, so on "zsh: command not found: astro" the
// trailing-name form would otherwise match the shell's own name ("zsh") first.
var notFoundRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)command not found:\s*([\w.@/+-]+)`),
	regexp.MustCompile(`(?m)([\w.@/+-]+): (?:command )?not found`),
}

// missingCommand returns the command a shell reported as not found, or "".
func missingCommand(output string) string {
	for _, re := range notFoundRes {
		if m := re.FindStringSubmatch(output); m != nil {
			return m[1]
		}
	}
	return ""
}

// depManager describes an ecosystem's "dependencies are not installed" shape:
// a manifest that says deps are expected, the directory they land in, and the
// command that installs them.
type depManager struct {
	manifest string
	depsDir  string
	lockfile string
	install  string
}

// depManagers is ordered so a more specific lockfile wins: a repo with both
// pnpm-lock.yaml and package-lock.json should be told to run pnpm.
var depManagers = []depManager{
	{manifest: "package.json", depsDir: "node_modules", lockfile: "pnpm-lock.yaml", install: "pnpm install --frozen-lockfile"},
	{manifest: "package.json", depsDir: "node_modules", lockfile: "yarn.lock", install: "yarn install --immutable"},
	{manifest: "package.json", depsDir: "node_modules", lockfile: "bun.lockb", install: "bun install --frozen-lockfile"},
	{manifest: "package.json", depsDir: "node_modules", lockfile: "package-lock.json", install: "npm ci"},
	{manifest: "package.json", depsDir: "node_modules", lockfile: "", install: "npm install"},
}

// classifyEnvFailure decides whether a failed step failed because it could not
// run at all, returning nil when the failure looks like a real verdict on the
// code. It is deliberately conservative: only an explicit "command not found"
// qualifies, because misclassifying a genuine failure as an environment problem
// would soften a real gate failure into an excuse.
func classifyEnvFailure(step domain.StepName, output, worktreeDir string) *envFailure {
	cmd := missingCommand(output)
	if cmd == "" {
		return nil
	}
	// Distinguish "this project's dependencies are not installed" from "this
	// tool is not on this machine". The former is the common case and has a
	// precise fix; the latter needs a real install.
	if dm, ok := uninstalledDeps(worktreeDir); ok {
		return &envFailure{
			Summary: string(step) + " could not run (dependencies not installed)",
			Message: string(step) + " could not run: `" + cmd + "` is not available because " +
				dm.depsDir + " is missing — the project's dependencies have never been installed here.\n" +
				"Nothing is wrong with your tree. Install them in the repository root and retry:\n  " + dm.install,
		}
	}
	return &envFailure{
		Summary: string(step) + " could not run (" + cmd + " not installed)",
		Message: string(step) + " could not run: `" + cmd + "` is not on PATH.\n" +
			"Nothing is wrong with your tree — install the tool, or point the step at one that exists.",
	}
}

// uninstalledDeps reports the ecosystem whose manifest is present in dir while
// its dependency directory is not — i.e. an environment nobody has installed.
func uninstalledDeps(dir string) (depManager, bool) {
	if dir == "" {
		return depManager{}, false
	}
	for _, dm := range depManagers {
		if !fileExists(filepath.Join(dir, dm.manifest)) {
			continue
		}
		if dirExists(filepath.Join(dir, dm.depsDir)) {
			continue // deps ARE installed; the failure is something else
		}
		if dm.lockfile == "" || fileExists(filepath.Join(dir, dm.lockfile)) {
			return dm, true
		}
	}
	return depManager{}, false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// looksLikeShellNotFound is a cheap pre-filter so the regex only runs on output
// that plausibly contains a not-found message.
func looksLikeShellNotFound(output string) bool {
	return strings.Contains(strings.ToLower(output), "not found")
}
