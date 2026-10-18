package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// skippedNoteGolden is the provenance note a gate with a skipped `review` step
// writes, with only the values that differ run to run (identities, clocks,
// hashes) masked. It was captured from the warden that predates the skip
// marker, so it is the independent statement of what the signed record looked
// like BEFORE the human-visible outcome existed. Regenerate only on a change
// that is meant to alter the note: WARDEN_E2E_UPDATE=1.
const skippedNoteGolden = "testdata/skipped_review_note.golden"

// volatileNoteFields masks the values a note legitimately varies by between
// runs. Everything else — key set, key order, step list, agent map, evidence
// kinds and their sequence — stays byte-for-byte.
var volatileNoteFields = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`"run_id":"[^"]*"`), `"run_id":"<run_id>"`},
	{regexp.MustCompile(`"timestamp":"[^"]*"`), `"timestamp":"<time>"`},
	{regexp.MustCompile(`"timestamp":\d+`), `"timestamp":0`},
	{regexp.MustCompile(`"warden_version":"[^"]*"`), `"warden_version":"<version>"`},
	{regexp.MustCompile(`"commit_sha":"[0-9a-f]+"`), `"commit_sha":"<sha>"`},
	{regexp.MustCompile(`"covers_from":"[0-9a-f]+"`), `"covers_from":"<sha>"`},
	{regexp.MustCompile(`"(hash|previous_hash|evidence_chain_root)":"[0-9a-f]+"`), `"$1":"<hash>"`},
	// The key is minted per test (WARDEN_CONFIG_DIR is a temp dir), so the
	// signer and its signature differ every run; the test verifies the
	// signature separately instead of comparing it.
	{regexp.MustCompile(`"(public_key|signature)":"[A-Za-z0-9+/=]+"`), `"$1":"<sig>"`},
}

func maskNote(raw string) string {
	out := strings.TrimSpace(raw)
	for _, v := range volatileNoteFields {
		out = v.re.ReplaceAllString(out, v.repl)
	}
	return out + "\n"
}

// TestE2E_SkippedAgentStepIsNamedNotPassed runs a real gate with `review`
// listed and no agent configured. Before the fix the output read
// "(lint, review)" — as though the review had run — when warden had executed
// nothing for it. The human-visible output must now say the review was
// skipped, and must do so WITHOUT touching the signed note: that record is
// what verifiers re-derive the signature over, so its bytes for this run must
// be exactly what they were before.
func TestE2E_SkippedAgentStepIsNamedNotPassed(t *testing.T) {
	g := newGoldenRepo(t)
	cfg := "hooks: { pre_push: true }\nsteps: { pre_push: [lint, review] }\ncommands: { lint: \"true\" }\nrules: []\n"
	if err := os.WriteFile(filepath.Join(g.dir, ".warden.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := g.warden("init"); code != 0 {
		t.Fatalf("init: %d %s", code, out)
	}
	if out, code := g.warden("trust", "add"); code != 0 {
		t.Fatalf("trust add: %d %s", code, out)
	}
	g.git("add", ".warden.yaml")
	g.git("commit", "--no-verify", "-m", "adopt warden")

	out, code := g.warden("run", "pre-push")
	// The verdict is unchanged: a skipped advisory step does not fail the gate.
	if code != 0 && code != 3 {
		t.Fatalf("gate must still pass with a skipped review, got %d: %s", code, out)
	}
	if !strings.Contains(out, "review: skipped, no agent configured") {
		t.Errorf("output must say the review was skipped, got:\n%s", out)
	}
	if strings.Contains(out, "(lint, review)") {
		t.Errorf("output still lists review as a step that ran:\n%s", out)
	}

	cmd := exec.Command("git", "notes", "--ref=warden", "show", "HEAD")
	cmd.Dir = g.dir
	note, err := cmd.Output()
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	got := maskNote(string(note))
	if os.Getenv("WARDEN_E2E_UPDATE") != "" {
		if err := os.MkdirAll(filepath.Dir(skippedNoteGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skippedNoteGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(skippedNoteGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("the signed note changed for an unchanged run:\n got: %s\nwant: %s", got, want)
	}
	// The masked comparison says the payload's shape is unchanged; verify says
	// the signature over the real bytes still holds.
	if vout, vcode := g.warden("verify", "--commit", "HEAD"); vcode != 0 {
		t.Errorf("the note no longer verifies (%d): %s", vcode, vout)
	}
	// Belt and braces against a future golden refresh hiding a payload change:
	// the outcome marker is a delivery-surface fact, never a note field.
	for _, leak := range []string{"skipped", "outcome", "no agent"} {
		if strings.Contains(string(note), leak) {
			t.Errorf("note carries %q — the marker leaked into the signed record: %s", leak, note)
		}
	}
}
