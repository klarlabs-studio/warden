package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// TestDispatch_VerifyCiAuditImport drives the distribution/provenance commands
// through the dispatcher in a real repo. It exercises the cli commands and the
// service methods behind them (Verify, Audit, ImportConfig, CIStatus) in one
// pass.
func TestDispatch_VerifyCiAuditImport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := gitRepo(t)
	chdir(t, dir)

	// import: a Makefile with lint/test targets should be detected.
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("lint:\n\ttrue\ntest:\n\ttrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("import"); code != 0 || !strings.Contains(out, "make lint") {
		t.Errorf("import: code=%d out=%q", code, out)
	}

	// init records the adoption point so verify/audit have a baseline.
	if code, _, errb := run("init"); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb)
	}

	// verify: HEAD carries no note → unvalidated → non-zero.
	if code, out, _ := run("verify"); code == 0 || !strings.Contains(out, "unverified") {
		t.Errorf("verify (no note): code=%d out=%q", code, out)
	}
	if code, out, _ := run("verify", "--quiet"); code == 0 || out != "" {
		t.Errorf("verify --quiet should be silent + non-zero: code=%d out=%q", code, out)
	}

	// audit: json + md render without gating (exit 0).
	code, out, _ := run("audit", "--format", "json")
	if code != 0 {
		t.Fatalf("audit json: code=%d out=%q", code, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Errorf("audit json is not valid JSON: %v\n%s", err, out)
	}
	if code, out, _ := run("audit", "--format", "md"); code != 0 || !strings.Contains(out, "|") {
		t.Errorf("audit md: code=%d out=%q", code, out)
	}
	if code, _, _ := run("audit", "--format", "bogus"); code != 2 {
		t.Errorf("bad --format should exit 2, got %d", code)
	}

	// ci: reports status for the branch's PR. With no PR (or no gh) it still
	// produces output and returns; exact code depends on the environment.
	if _, out, _ := run("ci"); !strings.Contains(out, "CI") && !strings.Contains(out, "gh") {
		t.Errorf("ci produced no recognizable output: %q", out)
	}
}

func TestCIHelpers(t *testing.T) {
	if !isTerminal(domain.CIPassing) || isTerminal(domain.CIPending) {
		t.Error("terminal classification wrong")
	}
	if ciExit(domain.CIFailing) != 1 || ciExit(domain.CIPassing) != 0 {
		t.Error("ci exit codes wrong")
	}
}
