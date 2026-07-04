package domain

import "strings"

// Ecosystem is one detected buildable unit: a language rooted at a path relative
// to the repo (Path "." is the repo root). A monorepo has several — e.g. a Go
// module at apps/api and a TypeScript app at web. Filesystem detection that
// produces these lives in infrastructure; the language→commands knowledge stays
// in LanguageCommands, so adding a language is a LanguageCommands entry, not a
// change here.
type Ecosystem struct {
	Lang Language
	Path string
}

// ComposeConfig turns detected ecosystems into a starter gate: a lint + test
// command per ecosystem (scoped to its path), an optional nox security scan,
// and pre-commit (fast: lints only) / pre-push (tests + lints + security) step
// lists. Each command is prefixed with `cd <path> &&` when the ecosystem isn't
// at the root — warden runs commands through `sh -c`, so no per-language or
// per-path special-casing is needed. Returns empty maps when nothing composes.
func ComposeConfig(ecos []Ecosystem, hasNox bool) (commands map[string]string, steps map[string][]StepName) {
	commands = map[string]string{}
	var lints, tests []StepName
	multi := len(ecos) > 1

	for _, e := range ecos {
		lc := LanguageCommands(e.Lang)
		if lc == nil {
			continue
		}
		prefix := ""
		if multi {
			prefix = ecosystemPrefix(e.Path) + "-"
		}
		for _, kind := range []string{"lint", "test"} {
			cmd := lc[kind]
			if cmd == "" {
				continue
			}
			name := prefix + kind
			commands[name] = scopeCommand(cmd, e.Path)
			if kind == "lint" {
				lints = append(lints, StepName(name))
			} else {
				tests = append(tests, StepName(name))
			}
		}
	}

	if hasNox {
		commands["security-scan"] = "nox scan . -severity-threshold high"
	}

	push := append(append([]StepName{}, tests...), lints...)
	if hasNox {
		push = append(push, StepName("security-scan"))
	}
	steps = map[string][]StepName{
		PreCommit.ConfigKey(): append([]StepName{}, lints...),
		PrePush.ConfigKey():   push,
	}
	return commands, steps
}

// scopeCommand runs cmd in the ecosystem's directory. warden execs each command
// via `sh -c`, so a plain `cd` prefix is enough — no nested quoting.
func scopeCommand(cmd, path string) string {
	if path == "" || path == "." {
		return cmd
	}
	return "cd " + path + " && " + cmd
}

// ecosystemPrefix turns a relative path into a step-name prefix: the root is
// "root"; a nested path collapses its separators (apps/api -> apps-api) so the
// name is readable and unique.
func ecosystemPrefix(path string) string {
	if path == "" || path == "." {
		return "root"
	}
	p := strings.ReplaceAll(path, "/", "-")
	p = strings.ReplaceAll(p, string('\\'), "-")
	return strings.Trim(p, "-")
}
