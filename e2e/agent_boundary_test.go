package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// standardConfig meets every standard@1 floor. steps is the pre_push list.
func standardConfig(extra string) string {
	return `hooks: { pre_push: true }
profile: standard
commands: { lint: "true", test: "true" }
steps:
  pre_commit: [credentials]
  pre_push: [credentials, lint, test, review]
rules: []
` + extra
}

// strictContainerConfig is strictConfig on the container image CI preloads.
// Strict needs its shell checks in a container, so every strict scenario that
// gets as far as running steps is gated on WARDEN_CONTAINER_TEST_IMAGE.
func strictContainerConfig(t *testing.T, extra string) string {
	t.Helper()
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	runtimeName := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if runtimeName == "" {
		runtimeName = "docker"
	}
	cfg := strings.Replace(strictConfig, `execution: { mode: container, image: "golang:1.26" }`,
		fmt.Sprintf("execution: { mode: container, image: %q, runtime: %q }", image, runtimeName), 1)
	return cfg + extra
}

// commitConfig writes cfg as .warden.yaml and commits it ungated, returning the
// remote's main before any gate runs.
func (g *goldenRepo) commitConfig(cfg string) string {
	g.t.Helper()
	g.writeFile(".warden.yaml", cfg)
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "configure the profile")
	return strings.TrimSpace(gitIn(g.t, g.remote, "rev-parse", "refs/heads/main"))
}

// Scenario 3 of ADR 0007: a profile that requires review gets a review or a
// refusal, never a pass that lists a review nobody ran. The refusal is a
// config problem (exit 78), names the profile and says how to configure an
// agent, and publishes nothing.
func TestGoldenFleet_SkippedAgentStepIsNotExecuted(t *testing.T) {
	check := func(t *testing.T, g *goldenRepo, before, profile string) {
		t.Helper()
		out, code := g.warden("run", "pre-push")
		if code != 78 {
			t.Fatalf("%s with review and no agent must exit 78, got %d: %s", profile, code, out)
		}
		for _, want := range []string{"profile " + profile + " requires review to run", "agent: claude"} {
			if !strings.Contains(out, want) {
				t.Errorf("the refusal must say %q: %s", want, out)
			}
		}
		g.assertNothingPublished(before)
	}
	t.Run("standard", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		check(t, g, g.commitConfig(standardConfig("")), "standard@1")
	})
	t.Run("strict", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		check(t, g, g.commitConfig(strictContainerConfig(t, "")), "strict@1")
	})
	// permissive keeps today's advisory skip: the gate passes and says so.
	t.Run("permissive", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		g.commitConfig("hooks: { pre_push: true }\nprofile: permissive\nsteps: { pre_push: [lint, review] }\ncommands: { lint: \"true\" }\nrules: []\n")
		out, code := g.warden("run", "pre-push")
		if code != 0 && code != 3 {
			t.Fatalf("permissive must still pass a skipped review, got %d: %s", code, out)
		}
		if !strings.Contains(out, "review: skipped, no agent configured") {
			t.Errorf("output must say the review was skipped: %s", out)
		}
	})
}

// Scenario 4: an agent that edits a tracked file and amends the commit cannot
// change what gets attested. Under standard it runs in a disposable clone, the
// guard sees HEAD move there, and the run fails: no note, the remote where it
// was, and the developer's branch and files untouched. Both scheduling paths
// are covered: a parallel batch, and a step running on its own (which used to
// get the validated worktree itself).
func TestGoldenFleet_AgentCannotRewriteTheAttestedCommit(t *testing.T) {
	// The marker is assembled at run time, so the config that names the command
	// does not itself contain it.
	const amender = `printf 'agent-%s\n' was-here >> .warden.yaml && git commit -q -a --amend --no-verify -m rewritten`
	for _, parallel := range []bool{true, false} {
		t.Run(fmt.Sprintf("parallel=%v", parallel), func(t *testing.T) {
			g := newGoldenRepo(t)
			g.adopt()
			extra := fmt.Sprintf("parallel: %v\nagent_commands: { faux: %q }\n", parallel, amender)
			cfg := strings.Replace(standardConfig(extra), "rules: []", "rules:\n  - then: { agent: { review: faux } }", 1)
			before := g.commitConfig(cfg)
			head := g.revParse("HEAD")

			out, code := g.warden("run", "pre-push")
			if code == 0 || code == 3 {
				t.Fatalf("an agent that amends the commit must fail the gate, got %d: %s", code, out)
			}
			if !strings.Contains(out, "moved HEAD") {
				t.Errorf("the failure must say the agent moved HEAD: %s", out)
			}
			g.assertNothingPublished(before)
			if got := g.revParse("HEAD"); got != head {
				t.Fatalf("the developer's branch moved: %s -> %s", head, got)
			}
			data, err := os.ReadFile(g.dir + "/.warden.yaml")
			if err != nil || string(data) != cfg {
				t.Fatalf("the agent's edit reached the developer's checkout: %v %q", err, data)
			}
			status := exec.Command("git", "status", "--porcelain", "--untracked-files=no")
			status.Dir = g.dir
			if st, err := status.CombinedOutput(); err != nil || strings.TrimSpace(string(st)) != "" {
				t.Fatalf("the developer's checkout is dirty: %v %s", err, st)
			}
		})
	}
}

// Scenario 5: strict hands an agent only the allowlist. A token exported in the
// shell that runs warden is visible to the agent under permissive (which keeps
// today's full environment) and absent under strict.
func TestGoldenFleet_AgentEnvIsScrubbed(t *testing.T) {
	const probe = `test -z "$FAKE_TOKEN"`
	token := []string{"FAKE_TOKEN=leaked-into-the-agent"}
	t.Run("permissive", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		before := g.commitConfig(fmt.Sprintf("hooks: { pre_push: true }\nprofile: permissive\nagent: claude\nagent_commands: { claude: %q }\nsteps: { pre_push: [review] }\nrules: []\n", probe))
		out, code := g.wardenEnv(token, "run", "pre-push")
		if code == 0 || code == 3 {
			t.Fatalf("under permissive the agent sees FAKE_TOKEN, so the probe must fail: %d %s", code, out)
		}
		g.assertNothingPublished(before)
	})
	t.Run("strict", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		g.commitConfig(strictContainerConfig(t, fmt.Sprintf("agent: claude\nagent_commands: { claude: %q }\n", probe)))
		out, code := g.wardenEnv(token, "run", "pre-push")
		if code != 0 && code != 3 {
			t.Fatalf("under strict the agent must not see FAKE_TOKEN: %d %s", code, out)
		}
		cmd := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
		cmd.Dir = g.dir
		if note, err := cmd.Output(); err != nil || !strings.Contains(string(note), `"review"`) {
			t.Fatalf("a passing strict gate must note the review it ran: %v %s", err, note)
		}
	})
}
