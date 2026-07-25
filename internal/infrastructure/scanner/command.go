package scanner

import (
	"path"
	"strings"
)

// Command is a configured security-scan command warden understands well enough
// to drive itself: it can re-point the scanner's report at a directory of
// warden's choosing and re-run the identical scan against another tree.
//
// Recognition is deliberately narrow. Warden only takes over a command whose
// every token it can account for, because the fallback (run it verbatim, gate
// on the exit code) is the old behavior and is always safe, whereas rewriting a
// command it half-understands is not. A `make audit`, an `npm audit`, or a nox
// invocation that already directs its own output stays on the old path.
type Command struct {
	// Raw is the command exactly as configured.
	Raw string
	// Binary is the scanner executable (usually "nox").
	Binary string
	// Threshold is the value of -severity-threshold, or "" when unset.
	Threshold string
	// Path is the scan target, relative to the tree being scanned.
	Path string
}

// ParseCommand recognizes a nox scan command. ok is false for anything warden
// should run verbatim instead.
func ParseCommand(raw string) (Command, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || hasShellMetachars(trimmed) {
		return Command{}, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !isNox(fields[0]) || fields[1] != "scan" {
		return Command{}, false
	}

	cmd := Command{Raw: trimmed, Binary: fields[0], Path: "."}
	seenPath := false
	for i := 2; i < len(fields); i++ {
		tok := fields[i]
		if !strings.HasPrefix(tok, "-") {
			if seenPath || !safeScanPath(tok) {
				return Command{}, false
			}
			cmd.Path, seenPath = tok, true
			continue
		}
		name, inlineValue, inline := strings.Cut(strings.TrimLeft(tok, "-"), "=")
		switch name {
		// -output and -format are the two flags warden must set itself to get a
		// report it can read. A command that already sets them has an intent of
		// its own (a SARIF upload, a report checked in somewhere); overriding it
		// would silently break that, so warden declines the command instead.
		case "output", "format":
			return Command{}, false
		case "severity-threshold":
			if inline {
				cmd.Threshold = inlineValue
				continue
			}
			if i+1 >= len(fields) {
				return Command{}, false
			}
			i++
			cmd.Threshold = fields[i]
		default:
			// Any other flag is carried through untouched; warden does not need
			// to understand it to re-run the same command elsewhere. Flags that
			// take a separate value are handled by the "not a flag" branch above
			// only when the value could be mistaken for a path, which
			// safeScanPath already rejects for anything exotic.
			if !inline && takesValue(name) {
				if i+1 >= len(fields) {
					return Command{}, false
				}
				i++
			}
		}
	}
	return cmd, true
}

// WithReportDir returns the command re-pointed at dir, emitting JSON. The
// scanner's flag parsing takes the last occurrence of a repeated flag, and
// ParseCommand has already refused any command that sets these itself, so
// appending is unambiguous.
func (c Command) WithReportDir(dir string) string {
	return c.Raw + " -format json -output " + shellQuote(dir)
}

// valueFlags are the scanner flags that take a separate argument. Getting this
// wrong would misread the flag's value as the scan path, which safeScanPath
// then rejects — so an omission here degrades to "warden runs the command
// verbatim", never to a wrong scan.
var valueFlags = map[string]bool{
	"baseline":            true,
	"changed-since":       true,
	"fingerprint-version": true,
	"history-depth":       true,
	"min-confidence":      true,
	"rules":               true,
	"severity-threshold":  true,
	"sort":                true,
	"tf-plan":             true,
	"vex":                 true,
}

func takesValue(flag string) bool { return valueFlags[flag] }

// isNox reports whether the executable is nox, allowing an absolute or relative
// path to it.
func isNox(bin string) bool {
	base := path.Base(strings.ReplaceAll(bin, `\`, "/"))
	return base == "nox" || base == "nox.exe"
}

// safeScanPath accepts only a relative in-tree path. An absolute path or one
// escaping the tree would make the base scan read the developer's live checkout
// instead of the materialized base tree, which would silently compare a tree
// against itself and report no delta at all.
func safeScanPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return false
	}
	clean := path.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// hasShellMetachars reports whether the command does anything beyond running one
// program with arguments. Warden appends flags to the command string, and
// appending to `nox scan . | tee log` or `nox scan . && echo ok` would attach
// them to the wrong program.
func hasShellMetachars(s string) bool {
	return strings.ContainsAny(s, "|&;<>()$`\\\"'\n")
}

// shellQuote wraps a path warden generated (a temp dir) for `sh -c`. The dirs
// are warden's own and contain no quotes; the guard is against spaces.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
