package cli

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

func profileRef(name string) *domain.ResolvedProfile {
	return &domain.ResolvedProfile{Name: name}
}

// The agent surface keeps auto-approving where it always has (no profile,
// permissive) and stops deciding under standard and strict, or under a profile
// this warden cannot read.
func TestAgentSurfaceApprover(t *testing.T) {
	cases := []struct {
		name       string
		profile    *domain.ResolvedProfile
		hook       domain.Hook
		wantApprov bool
		wantHint   string
	}{
		{"no profile", nil, domain.PrePush, true, ""},
		{"permissive", profileRef("permissive@1"), domain.PrePush, true, ""},
		{"standard", profileRef("standard@1"), domain.PrePush, false, "run `git push` from an interactive terminal"},
		{"strict", profileRef("strict@1"), domain.PrePush, false, "run `git push` from an interactive terminal"},
		{"strict pre-commit", profileRef("strict@1"), domain.PreCommit, false, "run `git commit` from an interactive terminal"},
		{"unreadable profile fails closed", profileRef("strict@9"), domain.PrePush, false, "strict@9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := agentSurfaceApprover{}.Approve(context.Background(), application.ApprovalRequest{
				Hook: c.hook, Branch: "main", Profile: c.profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			if c.wantApprov {
				if !d.Approved || d.NeedsHuman || d.Principal != "warden-auto" {
					t.Fatalf("want today's auto-approval, got %+v", d)
				}
				if d.Notice != agentSelfApprovalNotice {
					t.Errorf("a self-approval must announce the coming change (#295), notice = %q", d.Notice)
				}
				return
			}
			if d.Notice != "" {
				t.Errorf("a gate the surface did not approve needs no self-approval notice, got %q", d.Notice)
			}
			if d.Approved || !d.NeedsHuman {
				t.Fatalf("the agent surface must not satisfy the gate under %s: %+v", c.profile.Name, d)
			}
			if d.Principal != agentSurfacePrincipal {
				t.Errorf("principal = %q, want %q", d.Principal, agentSurfacePrincipal)
			}
			for _, want := range []string{"needs human approval", c.profile.Name, "nothing was pushed", "no note was written", "interactive terminal", c.wantHint} {
				if !strings.Contains(d.Rationale, want) {
					t.Errorf("rationale missing %q: %s", want, d.Rationale)
				}
			}
		})
	}
}

// On a non-interactive stream the terminal approver declines; it must not send
// a profiled repo's operator to the agent surface, which can no longer approve.
func TestTerminalApprover_NonInteractiveHintFollowsTheProfile(t *testing.T) {
	for _, c := range []struct {
		profile *domain.ResolvedProfile
		axi     bool
	}{
		{nil, true},
		{profileRef("permissive@1"), true},
		{profileRef("standard@1"), false},
	} {
		var out bytes.Buffer
		a := terminalApprover{in: strings.NewReader(""), out: &out}
		d, err := a.Approve(context.Background(), application.ApprovalRequest{Hook: domain.PrePush, Profile: c.profile})
		if err != nil || d.Approved {
			t.Fatalf("a non-interactive stream must decline: %+v %v", d, err)
		}
		if got := strings.Contains(out.String(), "warden axi"); got != c.axi {
			t.Errorf("profile %+v: mentions axi = %v, want %v: %s", c.profile, got, c.axi, out.String())
		}
	}
}

// standardApprovalConfig meets standard@1's floors and requires approval on
// main, so the only thing between an agent's run and a push is the gate.
const standardApprovalConfig = `profile: standard
hooks: { pre_push: true }
steps: { pre_push: [credentials, lint, test], pre_commit: [credentials] }
commands: { lint: "true", test: "true" }
rules:
  - match: { branch: main }
    then: { require_approval: true }
`

// Through the real axi verb: under standard the run ends needs_approval and
// says how a person approves; the same rule without a profile still passes.
func TestAxi_RunTriggerCannotSelfApproveUnderAProfile(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	// The rule matches main; git's default branch name varies by machine.
	if out, err := exec.Command("git", "-C", dir, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("branch -M main: %v %s", err, out)
	}
	writeConfig(t, dir, standardApprovalConfig)

	code, out, errb := run("axi", "run-trigger", "--trust")
	if code != 0 {
		t.Fatalf("axi run-trigger: code=%d out=%q err=%q", code, out, errb)
	}
	for _, want := range []string{"outcome: needs_approval", "push_performed: false", "needs human approval", "standard@1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}

	writeConfig(t, dir, strings.TrimPrefix(standardApprovalConfig, "profile: standard\n"))
	code, out, errb = run("axi", "run-trigger", "--trust")
	if code != 0 || !strings.Contains(out, "outcome: passed") {
		t.Fatalf("without a profile the agent surface must still auto-approve: code=%d out=%q err=%q", code, out, errb)
	}
	// ...and says, on the run it approved, that no person did and that this
	// is going away (#295).
	for _, want := range []string{"warnings[", "answered by this agent surface", "not by a person", "will end needs_approval"} {
		if !strings.Contains(out, want) {
			t.Errorf("an unprofiled self-approval must carry the notice; missing %q: %s", want, out)
		}
	}
}
