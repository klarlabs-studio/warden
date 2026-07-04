package detect

import (
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestEcosystems_MonorepoAndSkips(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/api/go.mod", "module x\n")
	write("web/package.json", "{}")
	write("web/tsconfig.json", "{}")
	// a dependency's manifest that must be pruned, not detected:
	write("web/node_modules/dep/package.json", "{}")

	got := map[string]domain.Language{}
	for _, e := range Ecosystems(root) {
		got[e.Path] = e.Lang
	}
	if got["apps/api"] != domain.LangGo {
		t.Errorf("apps/api = %q, want go", got["apps/api"])
	}
	if got["web"] != domain.LangTypeScript {
		t.Errorf("web = %q, want typescript (tsconfig present)", got["web"])
	}
	for p := range got {
		if filepath.Base(filepath.Dir(p)) == "node_modules" || p == "web/node_modules/dep" {
			t.Errorf("node_modules should be pruned, found %q", p)
		}
	}
}
