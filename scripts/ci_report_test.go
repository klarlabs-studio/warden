package scripts_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep the standalone reporter's regression suite in the ordinary Go CI gate.
func TestCIReportRegression(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(python, "-B", "-m", "unittest", "ci_report_test.py")
	cmd.Dir = filepath.Dir(repoFile(t, "scripts/ci_report.py"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CI reporting regressions: %v\n%s", err, out)
	}
}
