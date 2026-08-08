package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestCmdReattest(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir()) // local signer for the re-attestation
	// The source note below is signed by this key; pin it in the roster so it is a
	// trusted re-attestation source (an untrusted source is refused — see the
	// service-level reattest tests).
	pub, priv, _ := ed25519.GenerateKey(nil)
	fp := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(pub))
	dir := repoWithConfig(t, "trusted_keys:\n  - "+fp+"\n")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	head := func() string {
		s, err := svc.Repo().HeadSHA()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// A and B are tree-identical empty commits (the squash shape).
	git("commit", "--allow-empty", "--no-verify", "-m", "A")
	a := head()
	git("commit", "--allow-empty", "--no-verify", "-m", "B")
	b := head()

	// A carries a valid signed note from the pinned (trusted) key.
	rec := domain.RunRecord{
		RunID: "rA", CommitSHA: a, StepsRun: []domain.StepName{"lint"},
		EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}},
		PublicKey: base64.StdEncoding.EncodeToString(pub),
	}
	p, _ := rec.SigningPayload()
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))
	if err := svc.Repo().WriteNote(a, rec); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := cmdReattest([]string{"--commit", b}, &out, &errb); code != 0 {
		t.Fatalf("reattest b: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "re-attested") {
		t.Errorf("expected a re-attested message, got %q", out.String())
	}

	// A commit with a different tree has no validated twin → exit 1, no write.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "f.txt")
	git("commit", "--no-verify", "-m", "C")
	c := head()
	out.Reset()
	errb.Reset()
	if code := cmdReattest([]string{"--commit", c}, &out, &errb); code != 1 {
		t.Errorf("no-source reattest should exit 1, got %d (out=%q)", code, out.String())
	}
	if !strings.Contains(out.String(), "not re-attesting") {
		t.Errorf("expected a 'not re-attesting' message, got %q", out.String())
	}

	// --all audits the branch, so it needs an adoption point. Anchor it at A:
	// the window is then {B, C} — B was re-attested above and C has no source,
	// so the sweep has nothing left to write, which is success, not failure.
	if err := svc.Repo().WriteAdoption(a); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := cmdReattest([]string{"--all"}, &out, &errb); code != 0 {
		t.Fatalf("--all sweep: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing to re-attest") {
		t.Errorf("expected a 'nothing to re-attest' message, got %q", out.String())
	}
	// Without --push it must not claim to have touched the remote.
	if strings.Contains(out.String(), "pushed notes") {
		t.Errorf("a push-less sweep must not claim a push: %q", out.String())
	}

	// With --push the reader has to learn whether the remote is now current —
	// that is the question they ran the command to answer — even when the sweep
	// itself wrote nothing.
	out.Reset()
	errb.Reset()
	if code := cmdReattest([]string{"--all", "--push"}, &out, &errb); code != 0 {
		t.Fatalf("--all --push sweep: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "pushed notes to the remote") {
		t.Errorf("--push must report the publish attempt, got %q", out.String())
	}
}

// TestCmdReattest_AllWritesAndReports covers the sweep that actually finds
// something — the shape a maintainer runs after a squash-merge. The push-less
// run must both name every commit it closed and tell the reader the notes are
// still local, because a sweep that wrote notes nobody can fetch has not closed
// the gap it reports closing.
func TestCmdReattest_AllWritesAndReports(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir()) // local signer for the re-attestation
	pub, priv, _ := ed25519.GenerateKey(nil)
	fp := domain.KeyFingerprint(base64.StdEncoding.EncodeToString(pub))
	dir := repoWithConfig(t, "trusted_keys:\n  - "+fp+"\n")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	head := func() string {
		s, err := svc.Repo().HeadSHA()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// A is validated; B reproduces its tree (the squash shape) and has no note.
	git("commit", "--allow-empty", "--no-verify", "-m", "A")
	a := head()
	git("commit", "--allow-empty", "--no-verify", "-m", "B")
	b := head()

	rec := domain.RunRecord{
		RunID: "rA", CommitSHA: a, StepsRun: []domain.StepName{"lint"},
		EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}},
		PublicKey: base64.StdEncoding.EncodeToString(pub),
	}
	p, _ := rec.SigningPayload()
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))
	if err := svc.Repo().WriteNote(a, rec); err != nil {
		t.Fatal(err)
	}
	// Adopt before A so the sweep's window contains B.
	if err := svc.Repo().WriteAdoption(a); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := cmdReattest([]string{"--all"}, &out, &errb); code != 0 {
		t.Fatalf("--all sweep: code=%d err=%q", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "re-attested "+b[:12]) {
		t.Errorf("sweep should name the commit it closed, got %q", got)
	}
	if !strings.Contains(got, "1 commit(s) re-attested") {
		t.Errorf("sweep should report a count, got %q", got)
	}
	if !strings.Contains(got, "--push` to publish them") {
		t.Errorf("a push-less sweep must say the notes are still local, got %q", got)
	}

	// Re-attesting the same commit again is a no-op that says so — replacing a
	// sound note with another one is not something the command should do quietly.
	out.Reset()
	errb.Reset()
	if code := cmdReattest([]string{"--commit", b}, &out, &errb); code != 0 {
		t.Fatalf("second reattest: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "already carries a valid note") {
		t.Errorf("expected an already-attested message, got %q", out.String())
	}
}

// TestCmdReattest_UsageErrors pins exit 2 for the two ways the invocation itself
// is wrong, keeping them distinct from exit 1 ("nothing to re-attest"), which is
// a verdict rather than a mistake.
func TestCmdReattest_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"--nope"}},
		{"positional commit instead of --commit", []string{"HEAD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := cmdReattest(tc.args, &out, &errb); code != 2 {
				t.Errorf("code = %d, want 2; err=%q", code, errb.String())
			}
		})
	}
}
