package cli

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

const strictPolicyYAML = `profile: strict
agent: claude
execution: { mode: container, image: "golang:1.26" }
commands: { lint: "make lint", test: "make test", security-scan: "nox scan ." }
steps:
  pre_commit: [credentials, lint]
  pre_push: [rebase, review, credentials, lint, test, security-scan]
`

func TestPolicy_ExplainShowsProfileAndFloors(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, strictPolicyYAML)
	code, out, errb := run("policy", "explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("policy explain: code=%d err=%q", code, errb)
	}
	strict, _ := domain.ParseComplianceProfile("strict")
	for _, want := range []string{"profile:         strict@1 (" + strict.Digest() + ")", "floors:", "ok   container-execution", "ok   network-none", "review(agent=claude)"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}

	code, out, errb = run("axi", "policy-explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("axi policy-explain: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"profile", "strict@1", strict.Digest()} {
		if !strings.Contains(out, want) {
			t.Errorf("axi policy-explain missing %q: %q", want, out)
		}
	}
}

func TestPolicy_ExplainWithoutProfilePrintsNoProfile(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, "agent: claude\nsteps: { pre_push: [review, lint] }\ncommands: { lint: \"true\" }\n")
	code, out, errb := run("policy", "explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("policy explain: code=%d err=%q", code, errb)
	}
	if strings.Contains(out, "profile") || strings.Contains(out, "floors") || strings.Contains(out, "agent=") {
		t.Errorf("an unprofiled repo's explain must read as before:\n%s", out)
	}
	if _, out, _ = run("axi", "policy-explain", "--hook", "pre-push"); strings.Contains(out, "profile") {
		t.Errorf("axi output must not gain a profile key without one: %q", out)
	}
}

func TestPolicy_ExplainRefusesAConfigBelowItsProfile(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, strings.Replace(strictPolicyYAML, "mode: container", "mode: host", 1))
	code, _, errb := run("policy", "explain", "--hook", "pre-push")
	if code == 0 || !strings.Contains(errb, "execution.mode") || !strings.Contains(errb, "(set in .warden.yaml)") {
		t.Errorf("explain must surface the floor violation: code=%d err=%q", code, errb)
	}
}

func TestPrintProfile_FailingFloor(t *testing.T) {
	var b bytes.Buffer
	printProfile(&b, &domain.ResolvedProfile{Name: "strict@1", Digest: "sha256:x", Floors: []domain.ProfileFloorStatus{
		{ID: "network-none", Description: "execution.network is none", Problems: []string{`execution.network: is "bridge"; it must be none`}},
	}})
	out := b.String()
	if !strings.Contains(out, "FAIL network-none") || !strings.Contains(out, `is "bridge"`) {
		t.Errorf("a failing floor must be marked with its problem:\n%s", out)
	}
	b.Reset()
	printProfile(&b, nil)
	if b.Len() != 0 {
		t.Errorf("no profile prints nothing: %q", b.String())
	}
}
