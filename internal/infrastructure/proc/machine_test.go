package proc

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The id is generated once and then stable: every later call, in this process
// or another, reads the same value back from the config dir.
func TestMachineID_GeneratedOnceThenStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "warden")
	t.Setenv("WARDEN_CONFIG_DIR", dir)
	first, ok := MachineID()
	if !ok || len(first) != machineIDLen {
		t.Fatalf("MachineID = %q, %v; want a fresh %d-digit id", first, ok, machineIDLen)
	}
	if second, ok := MachineID(); !ok || second != first {
		t.Fatalf("second call = %q, %v; want the stored %q", second, ok, first)
	}
	raw, err := os.ReadFile(filepath.Join(dir, machineIDFile))
	if err != nil || string(raw) != first+"\n" {
		t.Fatalf("stored id %q (%v), want %q", raw, err, first)
	}
	// Another config dir is another machine: a different id.
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	if other, ok := MachineID(); !ok || other == first {
		t.Fatalf("a second config dir produced %q, %v; want a distinct id", other, ok)
	}
}

// Processes starting together on a new machine agree on one id.
func TestMachineID_ConcurrentFirstUseAgrees(t *testing.T) {
	dir := t.TempDir()
	ids := make([]string, 16)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _ = machineIDIn(dir)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("concurrent first use disagreed: %q", ids)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".machine-id-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %q", leftovers)
	}
}

// An id that cannot be read or created is no id: callers then sweep nothing.
// A malformed one is left as it is rather than replaced.
func TestMachineID_UnavailableIsNotOK(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WARDEN_CONFIG_DIR", filepath.Join(blocker, "warden"))
	if id, ok := MachineID(); ok || id != "" {
		t.Fatalf("an uncreatable config dir yielded %q, %v", id, ok)
	}

	dir := t.TempDir()
	t.Setenv("WARDEN_CONFIG_DIR", dir)
	path := filepath.Join(dir, machineIDFile)
	for _, content := range []string{"", "hand-edited\n", "0123456789ABCDEF0123456789ABCDEF\n", "0123456789abcdef\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if id, ok := MachineID(); ok {
			t.Errorf("malformed %q accepted as %q", content, id)
		}
		if raw, _ := os.ReadFile(path); string(raw) != content {
			t.Errorf("malformed id %q was overwritten with %q", content, raw)
		}
	}
}
