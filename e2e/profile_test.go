package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// strictConfig meets every strict@1 floor except where a scenario breaks one.
const strictConfig = `hooks: { pre_push: true }
profile: strict
execution: { mode: container, image: "golang:1.26" }
commands: { lint: "true", test: "true", security-scan: "true" }
steps:
  pre_commit: [credentials, lint]
  pre_push: [credentials, lint, test, security-scan, review]
rules: []
`

// assertNothingPublished checks the artifacts a refused gate must not have
// produced: no note anywhere, and the remote branch where it was.
func (g *goldenRepo) assertNothingPublished(remoteBefore string) {
	g.t.Helper()
	if got := strings.TrimSpace(gitIn(g.t, g.remote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		g.t.Fatalf("a refused config moved the remote: %s -> %s", remoteBefore, got)
	}
	for _, dir := range []string{g.dir, g.remote} {
		cmd := exec.Command("git", "-C", dir, "show-ref", "--verify", "refs/notes/warden")
		if out, err := cmd.CombinedOutput(); err == nil {
			g.t.Fatalf("a refused config wrote notes in %s: %s", dir, out)
		}
	}
}

func (g *goldenRepo) writeFile(rel, content string) {
	g.t.Helper()
	p := filepath.Join(g.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// A strict repo that runs its shell checks on the host does not get a gate that
// quietly runs them there anyway: the config fails to load, so nothing runs,
// nothing is noted and nothing is pushed.
func TestGoldenFleet_ProfileFloorRefusesHostExecution(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.writeFile(".warden.yaml", strings.Replace(strictConfig, "mode: container", "mode: host", 1))
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "strict, but on the host")
	before := strings.TrimSpace(gitIn(t, g.remote, "rev-parse", "refs/heads/main"))

	out, code := g.warden("run", "pre-push")
	if code == 0 || code == 3 {
		t.Fatalf("strict with execution.mode host must refuse the run: exit=%d %s", code, out)
	}
	if !strings.Contains(out, "execution.mode") || !strings.Contains(out, "(set in .warden.yaml)") || !strings.Contains(out, "strict@1") {
		t.Errorf("the refusal must name the field and the file: %s", out)
	}
	g.assertNothingPublished(before)
}

// An org base sets strict; a repo extending it writes standard. That is a load
// error naming both files, not a silent keep-the-base, so the author learns the
// line they wrote does nothing.
func TestGoldenFleet_ChildCannotLowerInheritedProfile(t *testing.T) {
	g := newGoldenRepo(t)
	g.adopt()
	g.writeFile("policy/base.yaml", strictConfig)
	g.writeFile(".warden.yaml", "extends: policy/base.yaml\nprofile: standard\n")
	g.git("add", ".warden.yaml", "policy/base.yaml")
	g.git("commit", "--no-verify", "-m", "try to relax the org profile")
	before := strings.TrimSpace(gitIn(t, g.remote, "rev-parse", "refs/heads/main"))

	out, code := g.warden("run", "pre-push")
	if code == 0 || code == 3 {
		t.Fatalf("a child lowering the inherited profile must refuse the run: exit=%d %s", code, out)
	}
	for _, want := range []string{"profile standard in .warden.yaml", "cannot lower strict", "policy/base.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal must say %q: %s", want, out)
		}
	}
	g.assertNothingPublished(before)
}

// unprofiledNoteGolden is the note a repo without `profile:` writes, captured
// from the warden that predates profiles. The config sets `agent: claude`,
// which was always inert; profiles wire it up only under a profile, so this run
// must not start an agent and its signed record must stay byte for byte.
const unprofiledNoteGolden = "testdata/unprofiled_agent_note.golden"

func TestGoldenFleet_UnprofiledRepoNoteIsUnchanged(t *testing.T) {
	g := newGoldenRepo(t)
	cfg := "hooks: { pre_push: true }\nagent: claude\nsteps: { pre_push: [lint, review] }\ncommands: { lint: \"true\" }\nrules: []\n"
	g.writeFile(".warden.yaml", cfg)
	if out, code := g.warden("init"); code != 0 {
		t.Fatalf("init: %d %s", code, out)
	}
	if out, code := g.warden("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %s", code, out)
	}
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "adopt warden")

	if out, code := g.warden("run", "pre-push"); code != 0 && code != 3 {
		t.Fatalf("an unprofiled gate must pass as before, got %d: %s", code, out)
	}
	cmd := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
	cmd.Dir = g.dir
	note, err := cmd.Output()
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	got := maskNote(string(note))
	if os.Getenv("WARDEN_E2E_UPDATE") != "" {
		if err := os.WriteFile(unprofiledNoteGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(unprofiledNoteGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("the note of an unprofiled repo changed:\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(string(note), "claude") || strings.Contains(strings.ToLower(string(note)), "profile") {
		t.Errorf("no agent and no profile may reach an unprofiled note: %s", note)
	}
	if vout, vcode := g.warden("verify", "--commit", "HEAD"); vcode != 0 {
		t.Errorf("the note no longer verifies (%d): %s", vcode, vout)
	}
}
