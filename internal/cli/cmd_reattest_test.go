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
}
