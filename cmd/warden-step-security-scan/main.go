// Command warden-step-security-scan is a Warden custom step that runs a nox
// security scan over the worktree. It speaks the stepsdk wire protocol, so it
// runs as an isolated subprocess — the same trust boundary any repo-authored
// step gets. Install it on PATH (as warden-step-security-scan) and reference the
// step name "security-scan" in .warden.yaml to wire it into the pipeline.
package main

import (
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/stepsdk"
)

func main() {
	stepsdk.Run(scan)
}

// scan runs `nox scan` against the worktree at high severity. nox applies the
// repo's committed .nox/baseline.json, so triaged false positives stay
// suppressed and only unwaived high/critical findings fail the step. nox's exit
// code is the verdict; its output becomes the finding detail. A missing nox
// binary is a skip, not a failure, so a contributor without nox installed can
// still commit — CI remains the backstop.
func scan(in stepsdk.Input) stepsdk.Output {
	if _, err := exec.LookPath("nox"); err != nil {
		return stepsdk.Pass()
	}

	cmd := exec.Command("nox", "scan", in.RepoPath, "-severity-threshold", "high", "-q")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return stepsdk.Pass()
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = "nox reported unwaived security findings"
	}
	return stepsdk.Fail(stepsdk.Finding{
		Severity: "high",
		Message:  "security-scan: " + msg,
	})
}
