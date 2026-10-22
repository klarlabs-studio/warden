package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadContainerObservation(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, content string
		valid         bool
	}{
		{"regular", "routes=0\nroutes6=0\n", true},
		{"oversized", strings.Repeat("x", maxObservationBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			raw, err := readContainerObservation(dir, tc.name)
			if tc.valid {
				if err != nil || string(raw) != tc.content {
					t.Fatalf("read = %q, %v", raw, err)
				}
			} else if err == nil || raw != nil {
				t.Fatalf("unsafe observation accepted: %d bytes, %v", len(raw), err)
			}
		})
	}
	for _, name := range []string{".", "missing"} {
		if _, err := readContainerObservation(dir, name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "regular"), filepath.Join(dir, "link")); err == nil {
		if _, err := readContainerObservation(dir, "link"); err == nil {
			t.Error("accepted symlink")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("routes=0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err == nil {
		if _, err := readContainerObservation(dir, "escape"); err == nil {
			t.Error("accepted external symlink")
		}
	}
}
