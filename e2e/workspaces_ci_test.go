package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The warden-verify action's scope outputs (design 0006, Phase B), against
// the binary under test and a repository built to a known provenance shape:
// the anchored fixture's full run validates every workspace; a leaf change's
// affected run still fails verification, and validates exactly the leaf and
// its dependent, which the per-workspace log independently says ran.

// actionOutputs runs the action's check script for HEAD in s and returns its
// GITHUB_OUTPUT entries.
func (s *scopeRepo) actionOutputs(key string) map[string]string {
	s.t.Helper()
	output := filepath.Join(s.t.TempDir(), "output")
	script, err := filepath.Abs(filepath.Join("..", ".github", "actions", "warden-verify", "verify.sh"))
	if err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = s.dir
	cmd.Env = append(hookEnv(), s.env("GITHUB_OUTPUT="+output, "WARDEN_COMMIT=HEAD", "WARDEN_TRUSTED_KEYS="+key)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.t.Fatalf("verify.sh: %v\n%s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		s.t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	return got
}

func workspacesOutput(t *testing.T, raw string) []string {
	t.Helper()
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		t.Fatalf("validated-workspaces %q is not a JSON array: %v", raw, err)
	}
	return paths
}

func TestGoldenFleet_ScopeVerifyActionOutputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	s := newAnchoredRepo(t)
	fp := s.fingerprint()

	got := s.actionOutputs(fp)
	if got["validated"] != "true" || got["scope"] != "full" || !reflect.DeepEqual(workspacesOutput(t, got["validated-workspaces"]), allExecWorkspaces) {
		t.Fatalf("anchored full run: %v", got)
	}

	s.change(leafChange, "leaf change")
	ran, rec, out := s.gate()
	want := []string{"apps/web", "packages/ui"}
	if !reflect.DeepEqual(ran, want) || !reflect.DeepEqual(testSelected(rec), want) {
		t.Fatalf("ran %v, note %v, want %v\n%s", ran, testSelected(rec), want, out)
	}
	got = s.actionOutputs(fp)
	if got["validated"] != "false" || got["scope"] != "affected" || !reflect.DeepEqual(workspacesOutput(t, got["validated-workspaces"]), want) {
		t.Fatalf("affected run: %v", got)
	}

	// The same note under a key that did not sign it is no claim at all.
	got = s.actionOutputs("bbbbbbbbbbbbbbbb")
	if got["validated"] != "false" || got["scope"] != "none" || got["validated-workspaces"] != "[]" {
		t.Fatalf("untrusted signer: %v", got)
	}
}
