package cli

import (
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

const workspacesPolicyYAML = `commands: { lint: "true" }
steps:
  pre_push: [lint, test]
workspaces:
  discover: [js, go]
  select: affected
  global: ["tools/build/**"]
  ignore: ["docs/**", ".github/**"]
per_workspace:
  test:
    each: "pnpm test --filter {workspace.name}"
    ecosystems: [js]
  typecheck:
    batch: "turbo run typecheck {workspaces.filter}"
`

func TestPolicy_ExplainShowsWorkspacesAndWhatRunsWhere(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, workspacesPolicyYAML)
	code, out, errb := run("policy", "explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("policy explain: code=%d err=%q", code, errb)
	}
	for _, want := range []string{
		"workspaces:      discover=js,go select=affected unowned=full concurrency=default",
		"  global:        tools/build/** (always: .npmrc",
		"  ignore:        docs/** .github/**",
		"per_workspace:",
		`  test: each "pnpm test --filter {workspace.name}" ecosystems=js (runs per workspace in this hook)`,
		`  typecheck: batch "turbo run typecheck {workspaces.filter}" (not in this hook's steps)`,
		"NOTE: " + domain.WorkspaceExecutionNotice(domain.PrePush, domain.WorkspaceSelectAffected) + ".",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}

	code, out, errb = run("axi", "policy-explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("axi policy-explain: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"workspaces_select", "affected", "per_workspace_steps", "typecheck", "per_workspace_active", "true", "trusted full-scope note"} {
		if !strings.Contains(out, want) {
			t.Errorf("axi policy-explain missing %q: %q", want, out)
		}
	}
}

func TestPolicy_ExplainWithoutWorkspacesIsUnchanged(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, "steps: { pre_push: [lint] }\ncommands: { lint: \"true\" }\n")
	_, out, _ := run("policy", "explain", "--hook", "pre-push")
	if strings.Contains(out, "workspace") || strings.Contains(out, "NOTE") {
		t.Errorf("a repo without workspaces must explain as before:\n%s", out)
	}
	if _, out, _ = run("axi", "policy-explain", "--hook", "pre-push"); strings.Contains(out, "workspace") {
		t.Errorf("axi output must not gain workspace keys without them: %q", out)
	}
}

func TestPolicy_ExplainRefusesAPlaceholderInCommands(t *testing.T) {
	dir := gitRepo(t)
	writeConfig(t, dir, "commands: { test: \"go test ./{workspace.path}/...\" }\n")
	code, _, errb := run("policy", "explain")
	if code == 0 || !strings.Contains(errb, "commands.test: {workspace.path} is only replaced in a per_workspace: command") {
		t.Errorf("a placeholder nothing replaces must be a load error: code=%d err=%q", code, errb)
	}
}
