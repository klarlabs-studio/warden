package cli

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
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

func TestPolicy_ExplainShowsWorkspacesAndThatTheyAreInactive(t *testing.T) {
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
		`  test: each "pnpm test --filter {workspace.name}" ecosystems=js (in this hook's steps: it does not run per workspace yet)`,
		`  typecheck: batch "turbo run typecheck {workspaces.filter}"`,
		"NOTE: " + domain.WorkspacePreviewNotice + ".",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "typecheck: batch \"turbo run typecheck {workspaces.filter}\" (in this hook's steps") {
		t.Errorf("typecheck is not in the pre-push steps:\n%s", out)
	}

	code, out, errb = run("axi", "policy-explain", "--hook", "pre-push")
	if code != 0 {
		t.Fatalf("axi policy-explain: code=%d err=%q", code, errb)
	}
	for _, want := range []string{"workspaces_select", "affected", "per_workspace_steps", "typecheck", "per_workspace_active", "false", "not active in this release"} {
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

func TestPrintWorkspacePreview(t *testing.T) {
	ws := &domain.ResolvedWorkspaces{
		Steps:  map[domain.StepName]domain.PerWorkspaceStep{"test": {Each: "x"}, "typecheck": {Batch: "y"}},
		Notice: domain.WorkspacePreviewNotice,
	}
	var b bytes.Buffer
	printWarnings(&b, application.RunResult{Policy: domain.ResolvedPolicy{Steps: []domain.StepName{"lint", "test"}, Workspaces: ws}})
	if want := "warden: per_workspace: defines test; this release does not run steps per workspace yet"; !strings.Contains(b.String(), want) {
		t.Errorf("a run step defined per workspace must be named:\n%s", b.String())
	}
	if strings.Contains(b.String(), "typecheck") {
		t.Errorf("only steps in this run are named:\n%s", b.String())
	}

	for name, p := range map[string]domain.ResolvedPolicy{
		"no workspaces":            {Steps: []domain.StepName{"test"}},
		"no per-workspace step":    {Steps: []domain.StepName{"lint"}, Workspaces: ws},
		"active per-workspace run": {Steps: []domain.StepName{"test"}, Workspaces: &domain.ResolvedWorkspaces{Active: true, Steps: ws.Steps}},
	} {
		b.Reset()
		printWorkspacePreview(&b, p)
		if b.Len() != 0 {
			t.Errorf("%s: want no notice, got %q", name, b.String())
		}
	}
}
