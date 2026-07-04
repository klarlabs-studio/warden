// Package detect infers a project's primary language from the marker files in
// its root, so `warden init` can pre-fill sensible lint/test commands. It is
// infrastructure: it reads the filesystem and maps what it finds to a domain
// Language. The language→commands knowledge itself lives in the domain.
package detect

import (
	"io/fs"
	"os"
	"path/filepath"

	"go.klarlabs.de/warden/internal/domain"
)

// skipDirs are dependency, build-output, and VCS directories that never hold a
// project we want to gate — and whose own manifests (a vendored go.mod, a
// dependency's package.json) would produce phantom ecosystems.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true, ".venv": true,
	"venv": true, "__pycache__": true, ".turbo": true, "testdata": true,
}

// Ecosystems walks root and returns every directory that looks like a buildable
// unit — a monorepo yields several (a Go module at apps/api, a TS app at web),
// a single-language repo yields one at ".". Dependency/build dirs are pruned.
// The per-directory language is resolved with the same priority order as
// Language, so a new language slots in via the markers table alone.
func Ecosystems(root string) []domain.Ecosystem {
	var out []domain.Ecosystem
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // best-effort walk; skip unreadable entries
		}
		if path != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if lang := Language(path); lang != domain.LangUnknown {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = "."
			}
			out = append(out, domain.Ecosystem{Lang: lang, Path: filepath.ToSlash(rel)})
		}
		return nil
	})
	return out
}

// marker pairs a filename with the language its presence implies.
type marker struct {
	file string
	lang domain.Language
}

// markers are checked in priority order: the first match wins, so a repo with
// both a backend go.mod and a frontend package.json resolves to its top-level
// build's language. Callers wanting per-directory policy use path-glob rules.
var markers = []marker{
	{"go.mod", domain.LangGo},
	{"Cargo.toml", domain.LangRust},
	{"pyproject.toml", domain.LangPython},
	{"setup.py", domain.LangPython},
	{"requirements.txt", domain.LangPython},
	// package.json is last so a polyglot repo's primary build language wins;
	// TypeScript is distinguished by a tsconfig.json alongside it.
	{"package.json", domain.LangJavaScript},
}

// Language inspects root and returns the detected project language, or
// LangUnknown when no marker is present.
func Language(root string) domain.Language {
	for _, m := range markers {
		if exists(filepath.Join(root, m.file)) {
			if m.lang == domain.LangJavaScript && exists(filepath.Join(root, "tsconfig.json")) {
				return domain.LangTypeScript
			}
			return m.lang
		}
	}
	return domain.LangUnknown
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
