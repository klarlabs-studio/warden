package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExecutionRequirementsRejectsMalformedPolicy(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":1,"steps":{},"unexpected":true}`, `{} {}`, `{"version":1,"version":2}`, strings.Repeat(" ", (1<<20)+1)} {
		file := filepath.Join(t.TempDir(), "requirements.json")
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadExecutionRequirements(file); err == nil {
			t.Fatal("accepted malformed policy")
		}
	}
}
