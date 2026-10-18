package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// agentCoAuthor is the trailer Claude Code writes by default (the display name
// is the model; the address is what identifies it).
const agentCoAuthor = "Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"

// Scenario 6 of ADR 0007: authorship signals are bound. A commit carrying an
// agent's Co-Authored-By trailer is listed in the signed note of a profiled
// gate; strict without an Agent Trace refuses it before anything runs or is
// published; and editing the declarations out of the note breaks the
// signature, so a verifier cannot be shown a note without them.
func TestGoldenFleet_AuthorshipSignalsAreBound(t *testing.T) {
	t.Run("the note lists the declaration", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.writeFile(".warden.yaml", "hooks: { pre_push: true }\nprofile: permissive\nsteps: { pre_push: [lint] }\ncommands: { lint: \"true\" }\nrules: []\n")
		if out, code := g.warden("init"); code != 0 {
			t.Fatalf("init: %d %s", code, out)
		}
		if out, code := g.warden("trust", "add"); code != 0 {
			t.Fatalf("trust add: %d %s", code, out)
		}
		g.git("add", ".warden.yaml")
		g.git("commit", "--no-verify", "-m", "adopt warden under a profile")
		g.git("commit", "--allow-empty", "--no-verify", "-m", "agent change\n\nbody\n\n"+agentCoAuthor)
		agentSHA := g.revParse("HEAD")
		g.git("commit", "--allow-empty", "--no-verify", "-m", "a later commit with no trailer")
		tip := g.revParse("HEAD")

		if out, code := g.wardenEnv([]string{"GEMINI_CLI=1"}, "run", "pre-push"); code != 0 && code != 3 {
			t.Fatalf("a permissive gate must pass, got %d: %s", code, out)
		}
		raw := g.readNote()
		var rec domain.RunRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("parse note: %v\n%s", err, raw)
		}
		if rec.CommitSHA != tip || rec.CoversFrom == "" {
			t.Fatalf("the note must attest the tip and claim the pushed span: %s", raw)
		}
		a := rec.Authorship
		if a == nil || len(a.Trailers) != 1 {
			t.Fatalf("authorship = %+v, want the agent commit's trailer: %s", a, raw)
		}
		want := domain.TrailerDeclaration{Commit: agentSHA, Agent: "claude-code", Trailer: agentCoAuthor}
		if a.Trailers[0] != want {
			t.Errorf("trailer declaration = %+v, want %+v", a.Trailers[0], want)
		}
		if len(a.EnvMarkers) != 1 || a.EnvMarkers[0] != (domain.EnvMarkerDeclaration{Name: "GEMINI_CLI", Agent: "gemini-cli"}) {
			t.Errorf("env markers = %+v, want GEMINI_CLI", a.EnvMarkers)
		}
		if strings.Contains(raw, "human") {
			t.Errorf("the note may not claim human authorship of anything: %s", raw)
		}
		if !rec.VerifySignature() {
			t.Fatal("the profiled note must verify")
		}
		if out, code := g.warden("verify", "--commit", "HEAD", "--key", rec.SignerFingerprint()); code != 0 {
			t.Fatalf("verify --key refused the note (%d): %s", code, out)
		}
		wout, _ := g.warden("why")
		if !strings.Contains(wout, "authorship:    agent declared:") || !strings.Contains(wout, agentCoAuthor) {
			t.Errorf("why must show the declaration:\n%s", wout)
		}

		// Editing the declarations out of the note fails the signature, both
		// in the record and for the real verifier.
		var forged domain.RunRecord
		_ = json.Unmarshal([]byte(raw), &forged)
		forged.Authorship = nil
		if forged.VerifySignature() {
			t.Fatal("a note with its declarations removed must fail its signature")
		}
		edited, err := json.Marshal(forged)
		if err != nil {
			t.Fatal(err)
		}
		note := exec.Command("git", "notes", "--ref=warden", "add", "-f", "-F", "-", "HEAD")
		note.Dir = g.dir
		note.Stdin = strings.NewReader(string(edited))
		if out, err := note.CombinedOutput(); err != nil {
			t.Fatalf("replace note: %v %s", err, out)
		}
		key := rec.SignerFingerprint()
		if out, code := g.warden("verify", "--commit", "HEAD", "--key", key); code == 0 {
			t.Fatalf("verify --key accepted a note whose declarations were edited out: %s", out)
		}
	})

	t.Run("strict without an Agent Trace fails", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		before := g.commitStrict()
		g.git("commit", "--allow-empty", "--no-verify", "-m", "agent change\n\n"+agentCoAuthor)

		out, code := g.warden("run", "pre-push")
		if code != 1 {
			t.Fatalf("strict with a declared agent and no trace must fail with 1, got %d: %s", code, out)
		}
		for _, want := range []string{"profile strict@1 requires an Agent Trace", agentCoAuthor, "agent_trace.path"} {
			if !strings.Contains(out, want) {
				t.Errorf("the refusal must say %q: %s", want, out)
			}
		}
		g.assertNothingPublished(before)
	})

	t.Run("strict refuses a gate run under an agent marker", func(t *testing.T) {
		g := newGoldenRepo(t)
		g.adopt()
		before := g.commitStrict()
		out, code := g.wardenEnv([]string{"CLAUDE_CODE_CHILD_SESSION=1"}, "run", "pre-push")
		if code != 1 || !strings.Contains(out, "the gate runs under CLAUDE_CODE_CHILD_SESSION (claude-code)") {
			t.Fatalf("an agent-run strict gate with no trace must fail and say why, got %d: %s", code, out)
		}
		g.assertNothingPublished(before)
	})
}

// commitStrict commits strictConfig ungated and returns the remote's main.
func (g *goldenRepo) commitStrict() string {
	g.t.Helper()
	g.writeFile(".warden.yaml", strictConfig)
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "adopt strict")
	return strings.TrimSpace(gitIn(g.t, g.remote, "rev-parse", "refs/heads/main"))
}
