package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// writeTree writes files (repo-relative, '/'-separated) into dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// commitTree writes files and commits them (and any deletions) on the current
// branch.
func commitTree(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	writeTree(t, dir, files)
	gitIn(t, dir, "add", "-A", "--", ".")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

// pnpmFixture is a pnpm monorepo: web depends on ui, ui on utils; api is
// independent.
var pnpmFixture = map[string]string{
	".warden.yaml":                "rules: []\n",
	"package.json":                `{"name":"root","private":true}`,
	"pnpm-workspace.yaml":         "packages:\n  - \"packages/*\"\n  - \"apps/*\"\n",
	"packages/utils/package.json": `{"name":"@acme/utils"}`,
	"packages/utils/index.ts":     "export {}\n",
	"packages/ui/package.json":    `{"name":"@acme/ui","dependencies":{"@acme/utils":"workspace:*"}}`,
	"packages/ui/index.ts":        "export {}\n",
	"apps/web/package.json":       `{"name":"web","devDependencies":{"@acme/ui":"workspace:^"}}`,
	"apps/api/package.json":       `{"name":"api"}`,
	"apps/api/server.ts":          "export {}\n",
}

// pnpmRepo builds the fixture on main and branches feat off it.
func pnpmRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, "")
	gitIn(t, dir, "branch", "-M", "main")
	commitTree(t, dir, "fixture", pnpmFixture)
	gitIn(t, dir, "checkout", "-q", "-b", "feat")
	return dir
}

func runWorkspaces(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(append([]string{"warden", "workspaces"}, args...), &out, &errb)
	return out.String(), errb.String(), code
}

func affectedJSON(t *testing.T, args ...string) service.AffectedWorkspaces {
	t.Helper()
	out, errOut, code := runWorkspaces(t, append([]string{"--affected", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("workspaces --affected: exit %d: %s", code, errOut)
	}
	var res service.AffectedWorkspaces
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return res
}

func TestCmdWorkspaces_ListsTheCommittedGraph(t *testing.T) {
	dir := pnpmRepo(t)
	// An untracked workspace on disk is not part of HEAD's graph.
	writeTree(t, dir, map[string]string{"packages/scratch/package.json": `{"name":"scratch"}`})

	out, errOut, code := runWorkspaces(t)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"4 workspace(s) at ", "apps/web", "packages/ui", "@acme/utils", "packages/utils", "DEPENDS ON"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "scratch") {
		t.Errorf("an untracked package.json reached the graph:\n%s", out)
	}

	out, _, code = runWorkspaces(t, "--json")
	if code != 0 {
		t.Fatal("--json failed")
	}
	var list service.WorkspaceList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	want := []domain.Workspace{
		{Path: "apps/api", Name: "api", Ecosystem: "js", Deps: []string{}},
		{Path: "apps/web", Name: "web", Ecosystem: "js", Deps: []string{"packages/ui"}},
		{Path: "packages/ui", Name: "@acme/ui", Ecosystem: "js", Deps: []string{"packages/utils"}},
		{Path: "packages/utils", Name: "@acme/utils", Ecosystem: "js", Deps: []string{}},
	}
	if got, _ := json.Marshal(list.Workspaces); string(got) != mustJSON(t, want) {
		t.Fatalf("workspaces = %s, want %s", got, mustJSON(t, want))
	}
	if list.Commit != headSHA(t) || !strings.HasPrefix(list.GraphDigest, "sha256:") {
		t.Fatalf("commit/digest = %q/%q", list.Commit, list.GraphDigest)
	}
}

func TestCmdWorkspaces_NoWorkspaces(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	repoWithConfig(t, "")
	out, _, code := runWorkspaces(t)
	if code != 0 || !strings.Contains(out, "no workspaces at") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestCmdWorkspaces_AffectedSelectsChangedAndDependents(t *testing.T) {
	dir := pnpmRepo(t)
	commitTree(t, dir, "touch utils", map[string]string{"packages/utils/index.ts": "export const x = 1\n"})

	res := affectedJSON(t, "--base", "main")
	sel := res.Selection
	if sel.Kind != domain.SelectionAffected || res.HeadWorkspaces != 4 {
		t.Fatalf("kind %s of %d, want affected of 4: %+v", sel.Kind, res.HeadWorkspaces, sel.Full)
	}
	if got := strings.Join(sel.Paths(), ","); got != "apps/web,packages/ui,packages/utils" {
		t.Fatalf("selected %s, want apps/web,packages/ui,packages/utils", got)
	}
	if r := sel.Workspaces[2].Reasons; len(r) != 1 || r[0].Kind != domain.ReasonChanged || r[0].Paths[0] != "packages/utils/index.ts" {
		t.Fatalf("utils reasons = %+v", r)
	}
	if r := sel.Workspaces[0].Reasons; len(r) != 1 || r[0].Kind != domain.ReasonDependent || r[0].Via[0] != "packages/ui" {
		t.Fatalf("web reasons = %+v", r)
	}
	main := strings.TrimSpace(gitOut(t, dir, "rev-parse", "main"))
	if res.Anchor != main || res.Head != headSHA(t) || res.Base != "main" {
		t.Fatalf("anchor/head/base = %s/%s/%s", res.Anchor, res.Head, res.Base)
	}
	if d, _ := sel.Digest(); d != res.SelectionDigest {
		t.Fatalf("selection digest %s does not match the selection (%s)", res.SelectionDigest, d)
	}

	out, errOut, code := runWorkspaces(t, "--affected", "--base", "main")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"affected: 3 of 4 workspace(s)", "changed: packages/utils/index.ts", "depends on: packages/ui", "selection sha256:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output lacks %q:\n%s", want, out)
		}
	}
}

// Commits that land on the base after the branch forked are not part of the
// change: the selection is measured from the merge-base.
func TestCmdWorkspaces_AffectedMeasuresFromTheMergeBase(t *testing.T) {
	dir := pnpmRepo(t)
	commitTree(t, dir, "touch api", map[string]string{"apps/api/server.ts": "export const y = 2\n"})
	gitIn(t, dir, "checkout", "-q", "main")
	commitTree(t, dir, "main moves", map[string]string{"packages/utils/index.ts": "export const z = 3\n"})
	gitIn(t, dir, "checkout", "-q", "feat")

	sel := affectedJSON(t, "--base", "main").Selection
	if got := strings.Join(sel.Paths(), ","); sel.Kind != domain.SelectionAffected || got != "apps/api" {
		t.Fatalf("selected %s (%s), want only apps/api", got, sel.Kind)
	}
}

func TestCmdWorkspaces_AffectedForcedFull(t *testing.T) {
	cases := map[string]struct {
		change  map[string]string
		rule    domain.FullRule
		message string
	}{
		"config file":   {map[string]string{".warden.yaml": "# reworded\nrules: []\n"}, domain.FullRuleConfiguration, ".warden.yaml changed"},
		"configuration": {map[string]string{".warden.yaml": "hooks: { pre_push: true }\nrules: []\n"}, domain.FullRuleConfiguration, "the effective configuration differs"},
		"root lockfile": {map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.0'\n"}, domain.FullRuleLockfile, "the root lockfile pnpm-lock.yaml changed"},
		"root manifest": {map[string]string{"pnpm-workspace.yaml": "packages: [\"packages/*\", \"apps/*\", \"tools/*\"]\n"}, domain.FullRuleRootManifest, "the root manifest pnpm-workspace.yaml changed"},
		"global path":   {map[string]string{"turbo.json": "{}\n"}, domain.FullRuleGlobal, `turbo.json matches the global pattern "turbo.json"`},
		"unowned path":  {map[string]string{"docs/readme.md": "hi\n"}, domain.FullRuleUnowned, "docs/readme.md belongs to no workspace"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := pnpmRepo(t)
			commitTree(t, dir, name, tc.change)
			res := affectedJSON(t, "--base", "main")
			if res.Selection.Kind != domain.SelectionFull || res.Selection.Full.Rule != tc.rule || len(res.Selection.Workspaces) != 4 {
				t.Fatalf("selection = %s %+v (%d selected), want full by %s", res.Selection.Kind, res.Selection.Full, len(res.Selection.Workspaces), tc.rule)
			}
			out, _, _ := runWorkspaces(t, "--affected", "--base", "main")
			if !strings.Contains(out, "full: all 4 workspace(s), because "+tc.message) {
				t.Fatalf("text output lacks the reason %q:\n%s", tc.message, out)
			}
		})
	}
}

// A workspace the change deletes still reaches what depended on it, and the
// text output names it.
func TestCmdWorkspaces_AffectedReportsRemovedWorkspaces(t *testing.T) {
	dir := pnpmRepo(t)
	gitIn(t, dir, "rm", "-q", "-r", "packages/utils")
	commitTree(t, dir, "drop utils", map[string]string{
		"packages/ui/package.json": `{"name":"@acme/ui"}`,
	})
	res := affectedJSON(t, "--base", "main")
	if got := strings.Join(res.Selection.Removed, ","); got != "packages/utils" {
		t.Fatalf("removed = %q, want packages/utils", got)
	}
	out, _, _ := runWorkspaces(t, "--affected", "--base", "main")
	if !strings.Contains(out, "removed: packages/utils") {
		t.Fatalf("text output does not name the removed workspace:\n%s", out)
	}
}

func TestCmdWorkspaces_DefaultBaseIsTheRemoteDefaultBranch(t *testing.T) {
	dir := pnpmRepo(t)
	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare")
	gitIn(t, dir, "remote", "add", "origin", remote)
	gitIn(t, dir, "push", "-q", "--no-verify", "origin", "main")
	gitIn(t, dir, "remote", "set-head", "origin", "main")
	commitTree(t, dir, "touch api", map[string]string{"apps/api/server.ts": "export const y = 2\n"})

	res := affectedJSON(t)
	if res.Base != "origin/main" || strings.Join(res.Selection.Paths(), ",") != "apps/api" {
		t.Fatalf("base %q selected %v, want origin/main and apps/api", res.Base, res.Selection.Paths())
	}
}

func TestCmdWorkspaces_InvalidManifestFailsLoudly(t *testing.T) {
	dir := pnpmRepo(t)
	commitTree(t, dir, "bad name", map[string]string{"apps/api/package.json": `{"name":"api $(touch pwned)"}`})
	_, errOut, code := runWorkspaces(t)
	if code != 1 || !strings.Contains(errOut, `invalid name "api $(touch pwned)"`) {
		t.Fatalf("exit %d, stderr %q; want 1 naming the invalid workspace", code, errOut)
	}
	_, errOut, code = runWorkspaces(t, "--affected", "--base", "main")
	if code != 1 || !strings.Contains(errOut, "workspaces at HEAD") {
		t.Fatalf("--affected: exit %d, stderr %q; want 1", code, errOut)
	}
}

func TestCmdWorkspaces_BadInvocations(t *testing.T) {
	pnpmRepo(t)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--base", "main"}, 2, "--base needs --affected"},
		{[]string{"extra"}, 2, "takes no positional arguments"},
		{[]string{"--nope"}, 2, "flag provided but not defined"},
		{[]string{"--affected", "--base", "-x"}, 1, "invalid base"},
		{[]string{"--affected", "--base", "no-such-branch"}, 1, "no common ancestor"},
		{[]string{"--affected"}, 1, "pass --base"},
	}
	for _, tc := range cases {
		_, errOut, code := runWorkspaces(t, tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.want) {
			t.Errorf("workspaces %v: exit %d, stderr %q; want %d containing %q", tc.args, code, errOut, tc.code, tc.want)
		}
	}
}

func TestCmdWorkspaces_OutsideARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, code := runWorkspaces(t); code != 1 {
		t.Fatalf("exit %d outside a repository, want 1", code)
	}
}

func TestForcedFullReason(t *testing.T) {
	for _, tc := range []struct {
		full *domain.ForcedFull
		want string
	}{
		{nil, "the selection is full"},
		{&domain.ForcedFull{Rule: domain.FullRuleConfiguration}, "differs from the anchor's"},
		{&domain.ForcedFull{Rule: domain.FullRuleInvalidPath, Path: "a/{x => y}"}, `"a/{x => y}" cannot be attributed`},
		{&domain.ForcedFull{Rule: "future-rule"}, "future-rule"},
	} {
		if got := forcedFullReason(tc.full); !strings.Contains(got, tc.want) {
			t.Errorf("forcedFullReason(%+v) = %q, want it to contain %q", tc.full, got, tc.want)
		}
	}
}

func TestSelectionReasons(t *testing.T) {
	got := selectionReasons([]domain.SelectionReason{
		{Kind: domain.ReasonChanged, Paths: []string{"a", "b", "c", "d", "e"}},
		{Kind: domain.ReasonDependent, Via: []string{"x", "y"}},
		{Kind: domain.ReasonFull},
	})
	if want := "changed: a, b, c (+2 more); depends on: x, y; full"; got != want {
		t.Fatalf("selectionReasons = %q, want %q", got, want)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
