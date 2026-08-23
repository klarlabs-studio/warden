package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// gpgSigner creates a throwaway GPG key in an isolated GNUPGHOME, configures
// the repo to sign commits with it, and returns its full fingerprint.
//
// A real key and a real signature, because the property under test is whether
// GIT can verify the signature — the thing that separates "a signature claiming
// to be the forge's" from "the forge signed this". A stubbed signature would
// test warden's opinion of a string and prove nothing about the boundary.
func gpgSigner(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not on PATH")
	}
	home := shortGPGHome(t)
	env := append(os.Environ(), "GNUPGHOME="+home)

	batch := filepath.Join(home, "params")
	// No passphrase, and an expiry so a stray key cannot outlive the test.
	params := "%no-protection\nKey-Type: eddsa\nKey-Curve: Ed25519\nName-Real: warden-forge-fixture\n" +
		"Name-Email: fixture@warden.invalid\nExpire-Date: 1d\n%commit\n"
	if err := os.WriteFile(batch, []byte(params), 0o600); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("gpg", "--batch", "--generate-key", batch)
	gen.Env = env
	if out, err := gen.CombinedOutput(); err != nil {
		// FATAL, not Skip. gpg is installed — LookPath said so — so a failure
		// here is this fixture being wrong, and skipping would report `ok` for
		// a suite in which the security cases never ran.
		t.Fatalf("gpg is installed but key generation failed: %v: %s", err, out)
	}
	t.Cleanup(func() {
		kill := exec.Command("gpgconf", "--kill", "all")
		kill.Env = env
		_ = kill.Run() // leaving an agent holding the temp dir open is untidy, not fatal
	})

	list := exec.Command("gpg", "--list-secret-keys", "--with-colons", "--fingerprint")
	list.Env = env
	out, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	var fpr string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			f := strings.Split(line, ":")
			if len(f) > 9 && len(f[9]) == 40 {
				fpr = f[9]
				break
			}
		}
	}
	if fpr == "" {
		t.Fatalf("could not read the fixture key fingerprint from:\n%s", out)
	}

	for _, kv := range [][2]string{
		{"user.signingkey", fpr},
		{"commit.gpgsign", "true"},
		{"gpg.program", "gpg"},
		// Explicit, because a developer's GLOBAL config may set gpg.format=ssh
		// (warden itself documents SSH signing), and the fixture repo inherits
		// it. Without this the commit fails with git's SSH-signer message,
		// "Couldn't load public key <fingerprint>", about an OpenPGP key.
		{"gpg.format", "openpgp"},
	} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = dir
		if o, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v: %s", kv[0], err, o)
		}
	}
	// Every later git call in this test must see the same keyring.
	t.Setenv("GNUPGHOME", home)
	return fpr
}

// The whole point: a commit the forge signed and warden never gated is reported
// as forge-authored, not as somebody's --no-verify push.
func TestVerifyRange_ForgeAuthoredIsNamedNotAccused(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	fpr := gpgSigner(t, dir)
	head := commit(t, dir, svc, "a commit the forge made")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{ForgeKeys: []string{fpr}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Commits) != 1 {
		t.Fatalf("range = %d commits, want 1", len(res.Commits))
	}
	v := res.Commits[0]
	if v.Reason != domain.ReasonForgeAuthored {
		t.Errorf("reason = %q, want %q", v.Reason, domain.ReasonForgeAuthored)
	}
	// Default policy still FAILS it. Naming the cause is not permission.
	if v.OK() || res.OK() {
		t.Error("ForgeReject is the default; a forge-authored commit must still fail")
	}
}

// With the policy on, it passes — and records which key vouched, so a green
// range cannot hide that warden ran nothing against this commit.
func TestVerifyRange_ForgeAcceptPassesAndRecordsTheSigner(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	fpr := gpgSigner(t, dir)
	head := commit(t, dir, svc, "a commit the forge made")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{
		ForgeKeys:   []string{fpr},
		ForgePolicy: domain.ForgeAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("ForgeAccept should pass a forge-signed commit: %+v", res.Commits)
	}
	if got := res.Commits[0].ForgeSigner; !strings.EqualFold(got, fpr) {
		t.Errorf("ForgeSigner = %q, want the key that signed it (%s)", got, fpr)
	}
}

// The security boundary. A commit signed by a key that is NOT pinned must not
// reach the forge path however permissive the policy — otherwise "accept the
// forge" would mean "accept anyone who signs".
func TestVerifyRange_ForgeAcceptRefusesAnUnpinnedSigner(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	_ = gpgSigner(t, dir) // the commit is signed, just not by the pinned key
	head := commit(t, dir, svc, "signed by a developer, not the forge")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{
		ForgeKeys:   []string{"5DE3E0509C47EA3CF04A42D34AEE18F83AFDEB23"},
		ForgePolicy: domain.ForgeAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("a signature from an unpinned key must not pass as forge-authored")
	}
	if got := res.Commits[0].Reason; got != domain.ReasonMissing {
		t.Errorf("reason = %q, want %q — an unpinned signer is just an un-noted commit", got, domain.ReasonMissing)
	}
}

// An UNSIGNED commit must never reach the forge path. This is the case a
// committer-name check would have got wrong: anyone can set committer.name to
// "GitHub", and if that were the test, `git -c user.name=GitHub commit` would
// walk through a required gate.
func TestVerifyRange_ForgeAcceptRefusesAnUnsignedCommit(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")

	// Impersonate the forge in every field a caller controls.
	for _, kv := range [][2]string{{"user.name", "GitHub"}, {"user.email", "noreply@github.com"}} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = dir
		if o, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git config: %v: %s", err, o)
		}
	}
	head := commit(t, dir, svc, "pretending to be the forge")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{
		ForgeKeys:   domain.GitHubWebFlowKeys,
		ForgePolicy: domain.ForgeAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("an unsigned commit claiming to be GitHub must not pass")
	}
	if got := res.Commits[0].Reason; got != domain.ReasonMissing {
		t.Errorf("reason = %q, want %q", got, domain.ReasonMissing)
	}
}

// Fail closed when the classification cannot run: no pinned keys means the
// caller never opted in, and the commit stays exactly as un-noted as it was.
func TestVerifyRange_NoPinnedKeysLeavesTheVerdictUntouched(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	_ = gpgSigner(t, dir)
	head := commit(t, dir, svc, "forge-signed, but nothing pinned")

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{ForgePolicy: domain.ForgeAccept})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("with no pinned forge keys nothing may be accepted as forge-authored")
	}
	if got := res.Commits[0].Reason; got != domain.ReasonMissing {
		t.Errorf("reason = %q, want %q", got, domain.ReasonMissing)
	}
}

// shortGPGHome returns a GNUPGHOME short enough for gpg-agent's socket.
//
// A Unix socket path caps around 104 bytes, and t.TempDir() on macOS is already
// ~90 of them before gpg appends S.gpg-agent — which surfaces as
// "can't connect to the gpg-agent: File name too long", then a skip, then a
// suite that prints ok having run none of the cases that matter.
func shortGPGHome(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"/tmp", os.TempDir()} {
		dir, err := os.MkdirTemp(base, "wg")
		if err != nil {
			continue
		}
		if len(dir) <= 40 {
			if err := os.Chmod(dir, 0o700); err != nil { // gpg refuses a group/world-readable home
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
		_ = os.RemoveAll(dir)
	}
	t.Fatal("no temp directory short enough for a gpg-agent socket")
	return ""
}

// The case CI actually runs in, and the attack it would enable.
//
// When the signing key is absent from the keyring git reports %G?=E and an
// EMPTY %GF — but %GK, the 64-bit key id, is still populated, because it is a
// field inside the signature packet rather than something git derived by
// verifying anything. Anyone can produce a signature carrying GitHub's key id.
//
// So an unverifiable signature must be worth nothing here however exactly its
// claimed identity matches. This test signs a commit, then throws the key away,
// and pins the very fingerprint that signed it: everything lines up except the
// one thing that counts, which is that nobody can check it.
func TestVerifyRange_ForgeAcceptRefusesASignatureItCannotVerify(t *testing.T) {
	dir, svc := newRepoSvc(t)
	base := commit(t, dir, svc, "base")
	fpr := gpgSigner(t, dir)
	head := commit(t, dir, svc, "signed, but the key will be gone")

	// Swap in an empty keyring — a fresh CI runner, which holds no public keys.
	t.Setenv("GNUPGHOME", shortGPGHome(t))

	res, err := svc.VerifyRange(base, head, RangeVerifyOptions{
		ForgeKeys:   []string{fpr}, // the exact key that signed it
		ForgePolicy: domain.ForgeAccept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatal("a signature warden could not verify must never pass as forge-authored, " +
			"however well its claimed key id matches")
	}
	if got := res.Commits[0].Reason; got != domain.ReasonMissing {
		t.Errorf("reason = %q, want %q — unverifiable is not forge-authored", got, domain.ReasonMissing)
	}
	if got := res.Commits[0].ForgeSigner; got != "" {
		t.Errorf("ForgeSigner = %q, want empty — nothing vouched for this commit", got)
	}
}
