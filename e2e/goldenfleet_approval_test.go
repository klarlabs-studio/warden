package e2e

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// approvalConfig is a pre-push policy whose rule requires approval on main and
// whose last step rewrites the branch, so a run that gets past the approval
// gate makes warden itself push: an approval the agent surface should not have
// given shows up on the remote, not only in an exit code. With profile set it
// meets standard@1's floors.
func approvalConfig(profile string) string {
	cfg := "hooks: { pre_push: true }\n" +
		"steps: { pre_push: [credentials, lint, test, amender], pre_commit: [credentials] }\n" +
		"commands:\n" +
		"  lint: \"true\"\n" +
		"  test: \"true\"\n" +
		"  amender: \"git commit -q --allow-empty --no-verify -m checked-rewrite\"\n" +
		"parallel: false\n" +
		"rules:\n" +
		"  - match: { branch: main }\n" +
		"    then: { require_approval: true }\n"
	if profile != "" {
		cfg = "profile: " + profile + "\n" + cfg
	}
	return cfg
}

// commitApprovalConfig adopts the repo with approvalConfig(profile) committed
// and returns the remote's main before the agent's run.
func (g *goldenRepo) commitApprovalConfig(profile string) string {
	g.t.Helper()
	g.adopt()
	if err := os.WriteFile(filepath.Join(g.dir, ".warden.yaml"), []byte(approvalConfig(profile)), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "require approval on main")
	return strings.TrimSpace(gitIn(g.t, g.remote, "rev-parse", "refs/heads/main"))
}

// hasWardenNotes reports whether dir (a work tree or a bare repo) has any
// provenance note ref at all.
func hasWardenNotes(dir string) bool {
	return exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet", "refs/notes/warden").Run() == nil
}

// Golden-fleet scenario 7. An agent starts the gate through `warden axi
// run-trigger --trust` on a rule that requires approval, under profile
// standard. The agent surface is the one asking for the run, so it may not also
// approve it: the run stops at the gate with outcome needs_approval, warden
// pushes nothing, writes no note, and tells the agent a person must approve.
//
// Before profiles made this distinction the surface approved itself as
// "warden-auto": the rewritten branch reached the remote with a note saying the
// gate passed.
func TestGoldenFleet_AgentSurfaceCannotSelfApprove(t *testing.T) {
	g := newGoldenRepo(t)
	before := g.commitApprovalConfig("standard")
	head := g.revParse("HEAD")

	out, code := g.warden("axi", "run-trigger", "--trust")
	if code != 0 {
		t.Fatalf("axi run-trigger must report the verdict as data: exit=%d %s", code, out)
	}
	if !strings.Contains(out, "outcome: needs_approval") {
		t.Fatalf("an agent-started run that needs approval must end needs_approval: %s", out)
	}
	if !strings.Contains(out, "push_performed: false") {
		t.Errorf("nothing may be pushed: %s", out)
	}
	for _, want := range []string{"needs human approval", "standard@1", "interactive terminal"} {
		if !strings.Contains(out, want) {
			t.Errorf("the agent must be told a person approves, and how (missing %q): %s", want, out)
		}
	}
	if got := strings.TrimSpace(gitIn(t, g.remote, "rev-parse", "refs/heads/main")); got != before {
		t.Fatalf("the agent approved its own run: remote main moved %s -> %s", before, got)
	}
	if got := g.revParse("HEAD"); got != head {
		t.Errorf("the local branch moved %s -> %s", head, got)
	}
	for _, dir := range []string{g.dir, g.remote} {
		if hasWardenNotes(dir) {
			t.Errorf("a run nobody approved wrote provenance notes in %s", dir)
		}
	}
}

// The same run without a profile is unchanged: the agent surface still
// auto-approves, so the rewrite is pushed and noted. Scenario 7 is a profile
// guarantee, not a new default (that proposal is tracked separately).
func TestGoldenFleet_AgentSurfaceStillAutoApprovesWithoutProfile(t *testing.T) {
	for _, profile := range []string{"", "permissive"} {
		t.Run("profile="+profile, func(t *testing.T) {
			g := newGoldenRepo(t)
			before := g.commitApprovalConfig(profile)

			out, code := g.warden("axi", "run-trigger", "--trust")
			if code != 0 || !strings.Contains(out, "outcome: passed") {
				t.Fatalf("without a withholding profile the agent surface must still auto-approve: exit=%d %s", code, out)
			}
			after := strings.TrimSpace(gitIn(t, g.remote, "rev-parse", "refs/heads/main"))
			if after == before {
				t.Fatalf("the approved rewrite was not pushed: remote main still %s\n%s", before, out)
			}
			if !hasWardenNotes(g.dir) {
				t.Errorf("the approved run wrote no note: %s", out)
			}
		})
	}
}

// pushUnderPTY runs `git push origin main` inside a pseudo-terminal made by
// script(1) and types answer at the terminal once the approval prompt shows.
// git's own output goes to push.log, so the pty transcript holds only what was
// written to the terminal itself. The answer waits for the prompt because
// script(1) forwards an early end of input as ^D, which would read as a decline.
func (g *goldenRepo) pushUnderPTY(answer string) (transcript, log string) {
	g.t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		g.t.Skip("script(1) not available to provide a pty")
	}
	logPath := filepath.Join(g.t.TempDir(), "push.log")
	inner := "git push origin main >'" + logPath + "' 2>&1"
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" || strings.HasSuffix(runtime.GOOS, "bsd") {
		cmd = exec.Command("script", "-q", "/dev/null", "sh", "-c", inner)
	} else {
		cmd = exec.Command("script", "-qec", inner, "/dev/null")
	}
	cmd.Dir = g.dir
	cmd.Env = append(hookEnv(), "WARDEN_CONFIG_DIR="+g.t.TempDir())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		g.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		g.t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		g.t.Fatal(err)
	}
	var seen strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Minute)
	for !strings.Contains(seen.String(), "approve? [y/N]") && time.Now().Before(deadline) {
		n, rerr := stdout.Read(buf)
		seen.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	if strings.Contains(seen.String(), "approve? [y/N]") {
		_, _ = io.WriteString(stdin, answer)
	}
	rest, _ := io.ReadAll(stdout)
	seen.Write(rest)
	_ = stdin.Close()
	_ = cmd.Wait()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		g.t.Fatalf("git push left no log (pty transcript: %s): %v", seen.String(), err)
	}
	return seen.String(), string(raw)
}

// installPushHook makes `git push` run the binary under test, as an adopted
// repo's hook does.
func (g *goldenRepo) installPushHook() {
	g.t.Helper()
	hook := "#!/bin/sh\nexec '" + strings.ReplaceAll(wardenBin, "'", "'\\''") + "' run pre-push\n"
	if err := os.WriteFile(filepath.Join(g.dir, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		g.t.Fatal(err)
	}
}

// With the agent surface closed under a profile, a person approves by running
// `git push` from a terminal. git hands the hook its ref list on stdin, so
// warden must ask on the controlling terminal: the prompt appears on the pty,
// not in git's output, "y" pushes and "n" leaves the remote alone.
func TestGoldenFleet_GitPushAsksOnTheControllingTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no controlling-terminal prompt on windows")
	}
	for _, c := range []struct {
		answer string
		pushed bool
	}{{"y\n", true}, {"n\n", false}} {
		t.Run(strings.TrimSpace(c.answer), func(t *testing.T) {
			g := newGoldenRepo(t)
			before := g.commitApprovalConfig("standard")
			g.installPushHook()
			// A plain policy: no rewrite, so after approval git completes the push.
			cfg := strings.Replace(approvalConfig("standard"), ", amender]", "]", 1)
			if err := os.WriteFile(filepath.Join(g.dir, ".warden.yaml"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			g.git("commit", "--no-verify", "-am", "no rewrite")
			head := g.revParse("HEAD")

			transcript, log := g.pushUnderPTY(c.answer)
			if !strings.Contains(transcript, "approve? [y/N]") {
				t.Fatalf("the prompt must appear on the terminal:\ntranscript: %s\nlog: %s", transcript, log)
			}
			if strings.Contains(log, "approve? [y/N]") {
				t.Errorf("the prompt leaked into git's output: %s", log)
			}
			got := strings.TrimSpace(gitIn(t, g.remote, "rev-parse", "refs/heads/main"))
			switch {
			case c.pushed && got != head:
				t.Errorf("an approved push must reach the remote: main=%s want %s\n%s\ntranscript: %q", got, head, log, transcript)
			case !c.pushed && got != before:
				t.Errorf("a declined push moved the remote %s -> %s\n%s", before, got, log)
			}
		})
	}
}
