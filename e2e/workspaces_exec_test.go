package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// Per-workspace execution, golden-fleet style. The fixture's shape fixes the
// right answer independently of warden: which workspaces a change affects is
// worked out by hand from the manifests below, and the assertion is on the
// log the commands themselves wrote — the workspaces that ACTUALLY ran — not
// on what warden says it ran.
//
// Shape (pnpm + go.work in one repository):
//
//	packages/utils  <- packages/ui <- apps/web
//	apps/api
//	apps/w $(touch "$LOG.pwned")   a directory name a pull request could add
//	lib <- svc                      (go.work: svc requires lib)

const weirdWorkspace = `apps/w $(touch "$LOG.pwned")`

func execFixture() map[string]string {
	return map[string]string{
		"package.json":                   `{"name":"root","private":true}`,
		"pnpm-workspace.yaml":            "packages:\n  - \"packages/*\"\n  - \"apps/*\"\n",
		"pnpm-lock.yaml":                 "lockfileVersion: '9.0'\n",
		"packages/utils/package.json":    `{"name":"@fx/utils"}`,
		"packages/utils/index.ts":        "export const u = 1;\n",
		"packages/ui/package.json":       `{"name":"@fx/ui","dependencies":{"@fx/utils":"workspace:*"}}`,
		"packages/ui/index.ts":           "export const ui = 1;\n",
		"apps/web/package.json":          `{"name":"web","dependencies":{"@fx/ui":"workspace:*"}}`,
		"apps/api/package.json":          `{"name":"api"}`,
		weirdWorkspace + "/package.json": `{"name":"weird"}`,
		"go.work":                        "go 1.22\n\nuse (\n\t./lib\n\t./svc\n)\n",
		"lib/go.mod":                     "module example.com/lib\n\ngo 1.22\n",
		"lib/lib.go":                     "package lib\n",
		"svc/go.mod":                     "module example.com/svc\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n",
		"svc/main.go":                    "package main\n",
		"README.md":                      "fixture\n",
		"docs/guide.md":                  "guide\n",
	}
}

var allExecWorkspaces = []string{"apps/api", weirdWorkspace, "apps/web", "lib", "packages/ui", "packages/utils", "svc"}

// The design's command shape, written so every placeholder stays outside
// any quotes of the command's own: warden quotes the value.
const execConfig = `steps: { pre_commit: [test], pre_push: [test] }
workspaces:
  discover: [js, go]
  select: affected
  ignore: ["docs/**"]
per_workspace:
  test:
    each: 'echo {workspace.path} >> "$LOG"'
`

type execRepo struct {
	*goldenRepo
	log string
}

func newExecRepo(t *testing.T, config string) *execRepo {
	t.Helper()
	g := newGoldenRepo(t)
	files := execFixture()
	files[".warden.yaml"] = config
	g.writeFiles(files)
	g.commit("fixture")
	g.git("push", "--no-verify", "origin", "main")
	return &execRepo{goldenRepo: g, log: filepath.Join(t.TempDir(), "ran.log")}
}

// run runs a hook with $LOG pointing at the repo's log and returns the
// output, the exit code and the workspaces that wrote to the log, sorted.
func (r *execRepo) run(hook string) (string, int, []string) {
	r.t.Helper()
	_ = os.Remove(r.log)
	out, code := r.wardenEnv([]string{"LOG=" + r.log}, "run", hook)
	data, err := os.ReadFile(r.log)
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	ran := []string{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			ran = append(ran, l)
		}
	}
	sort.Strings(ran)
	if _, err := os.Stat(r.log + ".pwned"); err == nil {
		r.t.Fatalf("a workspace path was executed as shell code:\n%s", out)
	}
	return out, code, ran
}

func (r *execRepo) stage(files map[string]string) { r.writeFiles(files) }

func TestGoldenFleet_PerWorkspacePreCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change map[string]string
		want   []string
	}{
		// A leaf's change runs it and what depends on it, nothing else.
		{"AffectedLeafChange", map[string]string{"packages/ui/index.ts": "export const ui = 2;\n"}, []string{"apps/web", "packages/ui"}},
		{"AffectedGoModule", map[string]string{"lib/lib.go": "package lib\n\nconst X = 1\n"}, []string{"lib", "svc"}},
		{"AffectedRoot", map[string]string{"packages/utils/index.ts": "export const u = 2;\n"}, []string{"apps/web", "packages/ui", "packages/utils"}},
		// Rules that force full run everything.
		{"LockfileForcesFull", map[string]string{"pnpm-lock.yaml": "lockfileVersion: '9.1'\n"}, allExecWorkspaces},
		{"UnownedPathForcesFull", map[string]string{"README.md": "changed\n"}, allExecWorkspaces},
		{"ConfigChangeForcesFull", map[string]string{".warden.yaml": execConfig + "# edited\n"}, allExecWorkspaces},
		// An ignored path selects nothing: the step runs for no workspace.
		{"IgnoredPathRunsNothing", map[string]string{"docs/guide.md": "edited\n"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newExecRepo(t, execConfig)
			r.stage(tc.change)
			out, code, ran := r.run("pre-commit")
			if code != 0 {
				t.Fatalf("pre-commit exited %d:\n%s", code, out)
			}
			if !reflect.DeepEqual(ran, tc.want) {
				t.Fatalf("ran %q\nwant %q\n%s", ran, tc.want, out)
			}
			if notes, _ := exec.Command("git", "-C", r.dir, "notes", "--ref=warden", "list").Output(); len(notes) != 0 {
				t.Fatalf("pre-commit wrote a note: %s", notes)
			}
		})
	}
}

// A config edit that is not staged still differs from HEAD's: the
// configuration in force decides, so the run is full. (An edit that leaves
// the parsed configuration equal, a comment, changes no digest and selects
// as before.)
func TestGoldenFleet_PerWorkspaceUnstagedConfigForcesFull(t *testing.T) {
	r := newExecRepo(t, execConfig)
	r.stage(map[string]string{"packages/ui/index.ts": "export const ui = 2;\n"})
	if err := os.WriteFile(filepath.Join(r.dir, ".warden.yaml"), []byte(strings.Replace(execConfig, `ignore: ["docs/**"]`, `ignore: ["docs/**", "notes/**"]`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code, ran := r.run("pre-commit")
	if code != 0 || !reflect.DeepEqual(ran, allExecWorkspaces) {
		t.Fatalf("exit %d, ran %q, want all\n%s", code, ran, out)
	}
}

// Pre-push runs per-workspace steps for every workspace, whatever select
// says, and the signed note's evidence chain holds one record per workspace.
func TestGoldenFleet_PerWorkspacePrePushRunsAll(t *testing.T) {
	r := newExecRepo(t, execConfig)
	r.stage(map[string]string{"packages/ui/index.ts": "export const ui = 2;\n"})
	r.git("commit", "--no-verify", "-q", "-m", "leaf change")
	out, code, ran := r.run("pre-push")
	if code != 0 && code != 3 {
		t.Fatalf("pre-push exited %d:\n%s", code, out)
	}
	if !reflect.DeepEqual(ran, allExecWorkspaces) {
		t.Fatalf("pre-push ran %q, want every workspace\n%s", ran, out)
	}
	note, err := exec.Command("git", "-C", r.dir, "notes", "--ref=warden", "show", "HEAD").Output()
	if err != nil {
		t.Fatalf("no note on HEAD: %v\n%s", err, out)
	}
	var rec struct {
		Evidence []struct {
			Kind string `json:"kind"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(note, &rec); err != nil {
		t.Fatalf("decode note: %v\n%s", err, note)
	}
	n := 0
	for _, e := range rec.Evidence {
		if e.Kind == "test.workspace" {
			n++
		}
	}
	if n != len(allExecWorkspaces) {
		t.Fatalf("note holds %d test.workspace records, want %d: %s", n, len(allExecWorkspaces), note)
	}
	// This release writes no workspace scope: the note is a plain full run,
	// which every verifier reads as before.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(note, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["scope"]; ok {
		t.Fatalf("pre-push wrote a scope, which this release must not: %s", note)
	}
	if out, code := r.warden("verify", "--commit", "HEAD"); code != 0 {
		t.Fatalf("the note must verify as a full run: %d %s", code, out)
	}
}

// A failing workspace fails the step, the output names it, and the other
// selected workspaces still ran.
func TestGoldenFleet_PerWorkspaceFailureNamesTheWorkspace(t *testing.T) {
	cfg := strings.Replace(execConfig, `each: 'echo {workspace.path} >> "$LOG"'`,
		`each: 'echo {workspace.path} >> "$LOG"; if [ {workspace.name} = web ]; then echo web-is-broken; exit 1; fi'`, 1)
	r := newExecRepo(t, cfg)
	r.stage(map[string]string{"packages/ui/index.ts": "export const ui = 2;\n"})
	out, code, ran := r.run("pre-commit")
	if code == 0 {
		t.Fatalf("a failing workspace must fail the commit:\n%s", out)
	}
	if !reflect.DeepEqual(ran, []string{"apps/web", "packages/ui"}) {
		t.Fatalf("ran %q\n%s", ran, out)
	}
	for _, want := range []string{"failed in workspace apps/web", "web-is-broken"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// Under container execution a per-workspace command runs inside the
// container, in the workspace's directory below the mounted worktree, with
// the workspace variables set — for an ordinary path and for one full of
// shell syntax. The command itself checks all three, so a pass is the
// evidence; the note must carry the container environment and one
// test.workspace record per workspace.
func TestGoldenFleet_PerWorkspaceInContainer(t *testing.T) {
	image := os.Getenv("WARDEN_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("set WARDEN_CONTAINER_TEST_IMAGE to a preloaded Linux image")
	}
	runtimeName := os.Getenv("WARDEN_CONTAINER_TEST_RUNTIME")
	if runtimeName == "" {
		runtimeName = "docker"
	}
	cfg := fmt.Sprintf(`execution: { image: %q, runtime: %q }
steps: { pre_push: [test] }
workspaces: { discover: [js] }
per_workspace:
  test:
    each: 'test "$(uname -s)" = Linux && test "$(pwd)" = /workspace/{workspace.path} && test "$WARDEN_WORKSPACE_PATH" = {workspace.path} && test "$WARDEN_WORKSPACE_NAME" = {workspace.name}'
`, image, runtimeName)
	g := newGoldenRepo(t)
	g.writeFiles(map[string]string{
		".warden.yaml":                   cfg,
		"package.json":                   `{"name":"root","private":true}`,
		"pnpm-workspace.yaml":            "packages:\n  - \"apps/*\"\n",
		"apps/web/package.json":          `{"name":"web"}`,
		weirdWorkspace + "/package.json": `{"name":"weird"}`,
	})
	g.commit("fixture")
	out, code := g.warden("run", "pre-push", "--attest-only")
	if code != 0 {
		t.Fatalf("container per-workspace gate: %d %s", code, out)
	}
	data, err := exec.Command("git", "-C", g.dir, "notes", "--ref=warden", "show", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	var record domain.RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if env, ok := record.Environments["test"]; !ok || env.Runtime != runtimeName {
		t.Fatalf("the per-workspace step's container environment is missing: %+v", record.Environments)
	}
	n := 0
	for _, e := range record.Evidence {
		if e.Kind == "test.workspace" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 test.workspace records, got %d: %s", n, data)
	}
}
