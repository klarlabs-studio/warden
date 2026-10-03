package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterpolationDoesNotHideLiteralCredential(t *testing.T) {
	dir := t.TempDir()
	token := "ghp_" + strings.Repeat("A", 36)
	for _, line := range []string{"//registry.npmjs.org/:_authToken=" + token + " # ${UNRELATED}", "${UNRELATED} " + token} {
		if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(line+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		got := scanFile(dir, ".npmrc")
		if len(got) != 1 {
			t.Fatalf("findings=%d", len(got))
		}
		if strings.Contains(got[0].Message, token) {
			t.Fatal("credential leaked into finding")
		}
	}
}
