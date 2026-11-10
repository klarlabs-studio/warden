package domain

// Language is a detected project language. It drives the default lint/test
// commands `warden init` pre-fills — knowledge that lives in the domain, while
// the filesystem detection that produces a Language is infrastructure.
type Language string

const (
	LangUnknown    Language = ""
	LangGo         Language = "go"
	LangRust       Language = "rust"
	LangJavaScript Language = "javascript"
	LangTypeScript Language = "typescript"
	LangPython     Language = "python"
)

// LanguageCommands returns sensible default lint/test commands for a language,
// or nil for an unknown one. Defaults prefer the toolchain's own built-ins over
// third-party tools a repo may not have installed, so an initialized gate works
// out of the box and the author tightens it from there. A fresh map is returned
// each call so callers can mutate it freely.
func LanguageCommands(l Language) map[string]string {
	switch l {
	case LangGo:
		return map[string]string{
			// gofmt -l names the files and exits 0 either way, so the check
			// has to fail on its output. The usual `test -z "$(gofmt -l .)"`
			// does that by swallowing the list — the one thing the author
			// needs — and the step then fails having printed nothing. This
			// keeps the names. One `&&`-joined expression, because
			// scopeCommand prefixes it with `cd <path> &&`; and no ": " in
			// it, so YAML writes it into .warden.yaml as a plain line rather
			// than a quoted one with every quote doubled.
			"lint": `gofmt -l . | awk '{print "gofmt needs to format " $0; bad=1} END {exit bad}' && go vet ./...`,
			"test": "go test ./...",
		}
	case LangRust:
		return map[string]string{
			"lint": "cargo fmt --check && cargo clippy --all-targets -- -D warnings",
			"test": "cargo test",
		}
	case LangJavaScript:
		return map[string]string{
			"lint": "npm run lint --if-present",
			"test": "npm test",
		}
	case LangTypeScript:
		return map[string]string{
			"lint": "npm run lint --if-present && npx --no-install tsc --noEmit",
			"test": "npm test",
		}
	case LangPython:
		return map[string]string{
			"lint": "ruff check .",
			"test": "pytest",
		}
	default:
		return nil
	}
}
