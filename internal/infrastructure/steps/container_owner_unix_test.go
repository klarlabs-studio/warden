//go:build unix

package steps

import (
	"os"
	"syscall"
	"testing"
)

func assertOwnedByInvoker(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		t.Fatalf("%s is not owned by uid %d: %+v", path, os.Getuid(), info.Sys())
	}
}
