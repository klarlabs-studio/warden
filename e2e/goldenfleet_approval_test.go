package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
