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

// The agent surface never satisfies the approval gate: not without a
// profile, not under permissive, and not under a profile this warden cannot
// read (#295).
func TestAgentSurfaceApprover(t *testing.T) {
	cases := []struct {
		name     string
		profile  *domain.ResolvedProfile
		hook     domain.Hook
		wantHint string
	}{
		{"no profile", nil, domain.PrePush, "run `git push` from an interactive terminal"},
		{"permissive", profileRef("permissive@1"), domain.PrePush, "run `git push` from an interactive terminal"},
		{"standard", profileRef("standard@1"), domain.PrePush, "run `git push` from an interactive terminal"},
		{"strict", profileRef("strict@1"), domain.PrePush, "run `git push` from an interactive terminal"},
		{"no profile pre-commit", nil, domain.PreCommit, "run `git commit` from an interactive terminal"},
		{"unreadable profile", profileRef("strict@9"), domain.PrePush, "run `git push` from an interactive terminal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := agentSurfaceApprover{}.Approve(context.Background(), application.ApprovalRequest{
				Hook: c.hook, Branch: "main", Profile: c.profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			if d.Approved || !d.NeedsHuman {
				t.Fatalf("the agent surface must not satisfy the gate: %+v", d)
			}
			if d.Principal != agentSurfacePrincipal {
				t.Errorf("principal = %q, want %q", d.Principal, agentSurfacePrincipal)
			}
			for _, want := range []string{"needs human approval", "cannot approve its own run", "nothing was pushed", "no note was written", c.wantHint} {
				if !strings.Contains(d.Rationale, want) {
					t.Errorf("rationale missing %q: %s", want, d.Rationale)
				}
			}
		})
	}
}

// On a non-interactive stream the terminal approver declines, and never sends
// the operator to the agent surface, which cannot approve.
func TestTerminalApprover_NonInteractiveHintNamesOnlyTheTerminal(t *testing.T) {
	for _, profile := range []*domain.ResolvedProfile{nil, profileRef("permissive@1"), profileRef("standard@1")} {
		var out bytes.Buffer
		a := terminalApprover{in: strings.NewReader(""), out: &out}
		d, err := a.Approve(context.Background(), application.ApprovalRequest{Hook: domain.PrePush, Profile: profile})
		if err != nil || d.Approved {
			t.Fatalf("a non-interactive stream must decline: %+v %v", d, err)
		}
		if strings.Contains(out.String(), "axi") || !strings.Contains(out.String(), "interactive terminal") {
			t.Errorf("profile %+v: the hint must name only the terminal: %s", profile, out.String())
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

// Through the real axi verb: a run that reaches the approval gate ends
// needs_approval and says how a person approves, with a profile and without
// one (#295).
func TestAxi_RunTriggerCannotSelfApprove(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := gitRepo(t)
	// The rule matches main; git's default branch name varies by machine.
	if out, err := exec.Command("git", "-C", dir, "branch", "-M", "main").CombinedOutput(); err != nil {
		t.Fatalf("branch -M main: %v %s", err, out)
	}
	for _, cfg := range []string{standardApprovalConfig, strings.TrimPrefix(standardApprovalConfig, "profile: standard\n")} {
		writeConfig(t, dir, cfg)
		code, out, errb := run("axi", "run-trigger", "--trust")
		if code != 0 {
			t.Fatalf("axi run-trigger: code=%d out=%q err=%q", code, out, errb)
		}
		for _, want := range []string{"outcome: needs_approval", "push_performed: false", "needs human approval", "cannot approve its own run"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q: %s", want, out)
			}
		}
	}
}
