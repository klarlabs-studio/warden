package detect

import (
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestLanguage(t *testing.T) {
	cases := []struct {
		name    string
		markers []string
		want    domain.Language
	}{
		{"go", []string{"go.mod"}, domain.LangGo},
		{"rust", []string{"Cargo.toml"}, domain.LangRust},
		{"python pyproject", []string{"pyproject.toml"}, domain.LangPython},
		{"python setup", []string{"setup.py"}, domain.LangPython},
		{"javascript", []string{"package.json"}, domain.LangJavaScript},
		{"typescript", []string{"package.json", "tsconfig.json"}, domain.LangTypeScript},
		{"none", nil, domain.LangUnknown},
		// Priority: a go.mod alongside a package.json resolves to Go.
		{"polyglot prefers go", []string{"go.mod", "package.json"}, domain.LangGo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, m := range c.markers {
				if err := os.WriteFile(filepath.Join(dir, m), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := Language(dir); got != c.want {
				t.Errorf("Language() = %q, want %q", got, c.want)
			}
		})
	}
}
