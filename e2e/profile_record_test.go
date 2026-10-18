package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// noOpStepsNoteGolden is the note an unprofiled repo writes when its gate has
// steps that pass without doing anything: a shell step with no command and an
// agent step with no agent. It was captured from the warden that predates the
// signed step outcomes, so it states independently what an unprofiled note
// looked like before profiles could add fields to the record. Regenerate only
// on a change that is meant to alter unprofiled notes: WARDEN_E2E_UPDATE=1.
const noOpStepsNoteGolden = "testdata/unprofiled_noop_steps_note.golden"

// readNote returns the raw provenance note on HEAD.
func (g *goldenRepo) readNote() string {
	g.t.Helper()
	cmd := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
	cmd.Dir = g.dir
	note, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("read note: %v", err)
	}
	return string(note)
}

// An unprofiled repo must get exactly the note it always got, even for the
// steps a profiled note now qualifies (a skipped shell step, a skipped agent
// step). The new signed fields are emitted only under a profile; if one leaked
// here, every pinned older verifier would start rejecting this repo's notes.
func TestGoldenFleet_UnprofiledNoteWithNoOpStepsIsUnchanged(t *testing.T) {
	g := newGoldenRepo(t)
	cfg := "hooks: { pre_push: true }\nsteps: { pre_push: [rebase, lint, test, review] }\ncommands: { lint: \"true\" }\nrules: []\n"
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
	note := g.readNote()
	got := maskNote(note)
	if os.Getenv("WARDEN_E2E_UPDATE") != "" {
		if err := os.WriteFile(noOpStepsNoteGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(noOpStepsNoteGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("the note of an unprofiled repo changed:\n got: %s\nwant: %s", got, want)
	}
	for _, leak := range []string{"profile", "step_outcomes", "approval", "skipped", "executed"} {
		if strings.Contains(note, leak) {
			t.Errorf("an unprofiled note carries %q: %s", leak, note)
		}
	}
	if vout, vcode := g.warden("verify", "--commit", "HEAD"); vcode != 0 {
		t.Errorf("the note no longer verifies (%d): %s", vcode, vout)
	}
}

// A profiled gate writes the profile, what happened to each step, and how the
// approval gate was answered into the signed note — and states only what it
// observed: the shell step with no command and the agent step with no agent
// are recorded as skipped, not executed, and no isolation is claimed.
func TestGoldenFleet_ProfiledNoteRecordsObservedOutcomes(t *testing.T) {
	g := newGoldenRepo(t)
	cfg := "hooks: { pre_push: true }\nprofile: permissive\nsteps: { pre_push: [lint, test, review] }\ncommands: { lint: \"true\" }\nrules: []\n"
	g.writeFile(".warden.yaml", cfg)
	if out, code := g.warden("init"); code != 0 {
		t.Fatalf("init: %d %s", code, out)
	}
	if out, code := g.warden("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %s", code, out)
	}
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "adopt warden under a profile")

	if out, code := g.warden("run", "pre-push"); code != 0 && code != 3 {
		t.Fatalf("a permissive gate must pass, got %d: %s", code, out)
	}
	raw := g.readNote()
	var rec domain.RunRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("parse note: %v\n%s", err, raw)
	}
	p, err := domain.ParseComplianceProfile("permissive")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Profile == nil || *rec.Profile != domain.NewProfileRef(p) {
		t.Errorf("profile = %+v, want permissive@1 with the built-in digest", rec.Profile)
	}
	want := map[domain.StepName]domain.StepObservation{
		domain.StepLint:   {Outcome: domain.StepExecuted},
		domain.StepTest:   {Outcome: domain.StepSkipped},
		domain.StepReview: {Outcome: domain.StepSkipped},
	}
	if !reflect.DeepEqual(rec.StepOutcomes, want) {
		t.Errorf("step outcomes = %+v, want %+v", rec.StepOutcomes, want)
	}
	if rec.Approval == nil || *rec.Approval != (domain.ApprovalRef{Channel: domain.ApprovalNone}) {
		t.Errorf("approval = %+v, want not required", rec.Approval)
	}
	for _, overclaim := range []string{"isolated", "tree_unchanged"} {
		if strings.Contains(raw, overclaim) {
			t.Errorf("the note claims %q, which nothing observed: %s", overclaim, raw)
		}
	}
	if !rec.VerifySignature() {
		t.Fatal("the profiled note must verify")
	}
	if vout, vcode := g.warden("verify", "--commit", "HEAD"); vcode != 0 {
		t.Errorf("verify refused the profiled note (%d): %s", vcode, vout)
	}
	wout, wcode := g.warden("why")
	if wcode != 0 {
		t.Fatalf("why: %d %s", wcode, wout)
	}
	for _, line := range []string{
		"outcomes:      lint executed, test skipped, review skipped",
		"profile:       permissive@1 (definition matches this warden's)",
		"approval:      not required",
	} {
		if !strings.Contains(wout, line) {
			t.Errorf("why must show %q:\n%s", line, wout)
		}
	}

	// The outcomes are covered by the signature: upgrading the skipped review
	// to executed in the note breaks it.
	forged := strings.Replace(raw, `"review":{"outcome":"skipped"}`, `"review":{"outcome":"executed"}`, 1)
	if forged == raw {
		t.Fatalf("test setup: the review outcome was not found in the note: %s", raw)
	}
	var tampered domain.RunRecord
	if err := json.Unmarshal([]byte(forged), &tampered); err != nil {
		t.Fatal(err)
	}
	if tampered.VerifySignature() {
		t.Error("a note whose review was rewritten to executed must fail its signature")
	}
}
