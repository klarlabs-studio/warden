package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The GitLab CI handshake, end to end, against the binary under test.
//
// A producer repository is gated by a real warden with a real signing key and
// publishes its notes to a bare remote. A consumer checkout is then built the
// way a GitLab runner builds one — `git init`, a GIT_DEPTH=1 fetch of the
// branch, a detached checkout, no notes — and the component's verify script
// runs in it exactly as GitLab would interpolate it. Each scenario's right
// answer is known from how the producer was built, not from what warden says.

var glInputRef = regexp.MustCompile(`\$\[\[\s*inputs\.([A-Za-z0-9_-]+)\s*\]\]`)

// glJobScript interpolates templates/verify.yml as GitLab does and returns the
// script item carrying the given marker from the named job.
func glJobScript(t *testing.T, job, part, marker string, inputs map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "templates", "verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	header, body, ok := strings.Cut(string(raw), "\n---\n")
	if !ok {
		t.Fatal("template has no spec header")
	}
	var spec struct {
		Spec struct {
			Inputs map[string]struct {
				Default string `yaml:"default"`
				Regex   string `yaml:"regex"`
			} `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(header), &spec); err != nil {
		t.Fatal(err)
	}
	body = glInputRef.ReplaceAllStringFunc(body, func(ref string) string {
		name := glInputRef.FindStringSubmatch(ref)[1]
		in := spec.Spec.Inputs[name]
		v, set := inputs[name]
		if !set {
			return in.Default
		}
		if in.Regex != "" && !regexp.MustCompile(in.Regex).MatchString(v) {
			t.Fatalf("GitLab would reject %s=%q", name, v)
		}
		return v
	})
	var jobs map[string]struct {
		Script       []string `yaml:"script"`
		BeforeScript []string `yaml:"before_script"`
	}
	if err := yaml.Unmarshal([]byte(body), &jobs); err != nil {
		t.Fatal(err)
	}
	items := jobs[job].Script
	if part == "before_script" {
		items = jobs[job].BeforeScript
	}
	for _, item := range items {
		if strings.Contains(item, "# warden: "+marker+"\n") {
			return item
		}
	}
	t.Fatalf("%s.%s has no item marked %q", job, part, marker)
	return ""
}

// glProducer is a developer repository gated by warden with a stable key.
type glProducer struct {
	*goldenRepo
	cfgDir string
}

func newGLProducer(t *testing.T) *glProducer {
	t.Helper()
	p := &glProducer{goldenRepo: newGoldenRepo(t), cfgDir: t.TempDir()}
	p.adopt()
	return p
}

// gate runs the real pre-push pipeline with cfgDir as the signing home and
// publishes branch and notes.
func (p *glProducer) gate(cfgDir string) {
	p.t.Helper()
	out, code := p.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + cfgDir}, "run", "pre-push")
	if code != 0 && code != 3 {
		p.t.Fatalf("gate: %d %s", code, out)
	}
	p.publish()
}

func (p *glProducer) publish() {
	p.t.Helper()
	p.git("push", "--no-verify", "origin", "main")
	p.git("push", "--no-verify", "origin", "refs/notes/warden")
}

func (p *glProducer) fingerprint() string {
	p.t.Helper()
	out, code := p.wardenEnv([]string{"WARDEN_CONFIG_DIR=" + p.cfgDir}, "key", "show")
	if code != 0 {
		p.t.Fatalf("key show: %d %s", code, out)
	}
	m := regexp.MustCompile(`fingerprint:\s*([0-9a-f]+)`).FindStringSubmatch(out)
	if m == nil {
		p.t.Fatalf("no fingerprint in: %s", out)
	}
	return m[1]
}

// glGit runs git in a working tree (gitIn addresses a git dir, for bare repos).
func glGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// glCheckout builds a consumer checkout the way a GitLab runner does with
// GIT_DEPTH=1: shallow, detached, and without refs/notes/warden.
func glCheckout(t *testing.T, remote, sha string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "file://" + remote},
		{"fetch", "-q", "--depth", "1", "origin", "+refs/heads/main:refs/remotes/origin/main"},
		{"checkout", "-q", "--detach", sha},
	} {
		glGit(t, dir, args...)
	}
	if strings.TrimSpace(glGit(t, dir, "rev-parse", "--is-shallow-repository")) != "true" {
		t.Fatal("fixture checkout is not shallow")
	}
	if err := exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet", "refs/notes/warden").Run(); err == nil {
		t.Fatal("fixture checkout already has notes")
	}
	return dir
}

// glVerify runs the component's verify script in checkout.
func glVerify(t *testing.T, checkout string, inputs map[string]string) (string, int, map[string]string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.Symlink(wardenBin, filepath.Join(bin, "warden")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", glJobScript(t, "warden-verify", "script", "verify", inputs))
	cmd.Dir = checkout
	cmd.Env = append(os.Environ(),
		"WARDEN_BIN_DIR="+bin,
		"CI_PROJECT_DIR="+checkout,
		"WARDEN_CONFIG_DIR="+t.TempDir(),
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	data, rerr := os.ReadFile(filepath.Join(checkout, "warden-verify.env"))
	if rerr != nil {
		t.Fatalf("no verdict file after exit %d: %s", code, out)
	}
	verdict := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		verdict[k] = v
	}
	return string(out), code, verdict
}

func assertGLVerdict(t *testing.T, out string, code int, v map[string]string, wantCode int, validated, verdict, sha string) {
	t.Helper()
	if code != wantCode || v["WARDEN_VALIDATED"] != validated || v["WARDEN_VERDICT"] != verdict || v["WARDEN_VERIFIED_COMMIT"] != sha {
		t.Fatalf("exit %d verdict %v; want exit %d %s/%s for %s\n%s", code, v, wantCode, validated, verdict, sha, out)
	}
}

func TestGitLabCI_ValidNotePassesFromAShallowCheckout(t *testing.T) {
	p := newGLProducer(t)
	p.commit("change")
	p.gate(p.cfgDir)
	sha := p.revParse("HEAD")

	checkout := glCheckout(t, p.remote, sha)
	out, code, v := glVerify(t, checkout, map[string]string{"key": p.fingerprint()})
	assertGLVerdict(t, out, code, v, 0, "true", "validated", sha)
	// The notes had to be fetched explicitly: the runner's clone has none.
	glGit(t, checkout, "show-ref", "--verify", "refs/notes/warden")

	// The other half of the handshake: a downstream job whose checkout
	// received the verdict as an artifact skips its checks; one whose commit
	// differs, or that received no verdict, runs them.
	helper := glJobScript(t, ".warden-skip-if-verified", "before_script", "skip-if-verified", nil) + "\necho RAN-CHECKS\n"
	downstream := glCheckout(t, p.remote, sha)
	artifact, err := os.ReadFile(filepath.Join(checkout, "warden-verify.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downstream, "warden-verify.env"), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", helper)
	cmd.Dir = downstream
	if got, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(got), "RAN-CHECKS") {
		t.Fatalf("validated commit did not skip (%v): %s", err, got)
	}
	cmd = exec.Command("sh", "-c", helper)
	cmd.Dir = glCheckout(t, p.remote, sha)
	if got, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(got), "RAN-CHECKS") {
		t.Fatalf("a job without the verdict skipped (%v): %s", err, got)
	}
}

func TestGitLabCI_WrongKeyFails(t *testing.T) {
	p := newGLProducer(t)
	p.commit("change")
	p.gate(p.cfgDir)
	sha := p.revParse("HEAD")
	out, code, v := glVerify(t, glCheckout(t, p.remote, sha), map[string]string{"key": "aaaaaaaaaaaaaaaa"})
	assertGLVerdict(t, out, code, v, 1, "false", "unverified", sha)
	if !strings.Contains(out, "untrusted") {
		t.Errorf("expected warden's untrusted-signer reason: %s", out)
	}
}

func TestGitLabCI_UnsignedNoteFails(t *testing.T) {
	p := newGLProducer(t)
	p.commit("change")
	// A config dir that cannot exist: signing is best-effort, so the gate still
	// writes a note — an unsigned one.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.gate(blocked)
	sha := p.revParse("HEAD")
	note := glGit(t, p.dir, "notes", "--ref=warden", "show", sha)
	if strings.Contains(note, `"signature"`) {
		t.Fatalf("fixture note is signed: %s", note)
	}
	out, code, v := glVerify(t, glCheckout(t, p.remote, sha), map[string]string{"key": p.fingerprint()})
	assertGLVerdict(t, out, code, v, 1, "false", "unverified", sha)
}

func TestGitLabCI_MissingNoteFails(t *testing.T) {
	p := newGLProducer(t)
	p.commit("gated")
	p.gate(p.cfgDir)
	p.commit("pushed with --no-verify")
	p.git("push", "--no-verify", "origin", "main")
	sha := p.revParse("HEAD")
	key := p.fingerprint()

	out, code, v := glVerify(t, glCheckout(t, p.remote, sha), map[string]string{"key": key})
	assertGLVerdict(t, out, code, v, 1, "false", "unverified", sha)

	// The provenance-skip mode reports the same verdict without failing, so
	// the downstream checks run.
	out, code, v = glVerify(t, glCheckout(t, p.remote, sha), map[string]string{"key": key, "on-unverified": "run-checks"})
	assertGLVerdict(t, out, code, v, 0, "false", "unverified", sha)
}

func TestGitLabCI_RemoteWithoutNotesFails(t *testing.T) {
	g := newGoldenRepo(t)
	g.commit("never gated")
	g.git("push", "--no-verify", "origin", "main")
	sha := g.revParse("HEAD")
	out, code, v := glVerify(t, glCheckout(t, g.remote, sha), map[string]string{"key": "aaaaaaaaaaaaaaaa"})
	assertGLVerdict(t, out, code, v, 1, "false", "no-notes", sha)
}

// A signed note copied onto another commit binds to the commit it was written
// for. The signature is valid and the signer trusted; the commit is not.
func TestGitLabCI_NoteForADifferentCommitFails(t *testing.T) {
	p := newGLProducer(t)
	p.commit("gated")
	p.gate(p.cfgDir)
	gated := p.revParse("HEAD")
	p.commit("not gated")
	target := p.revParse("HEAD")
	p.git("notes", "--ref=warden", "copy", gated, target)
	p.publish()

	key := p.fingerprint()
	out, code, v := glVerify(t, glCheckout(t, p.remote, target), map[string]string{"key": key})
	assertGLVerdict(t, out, code, v, 1, "false", "unverified", target)

	// Control: the commit the note was written for still verifies, so the
	// refusal above is about binding, not a broken fixture. It is the parent
	// of the shallow checkout's tip, so it is not present until the script
	// fetches it.
	checkout := glCheckout(t, p.remote, target)
	if err := exec.Command("git", "-C", checkout, "cat-file", "-e", gated+"^{commit}").Run(); err == nil {
		t.Fatal("fixture: the gated commit is already in the shallow checkout")
	}
	out, code, v = glVerify(t, checkout, map[string]string{"key": key, "commit": gated})
	assertGLVerdict(t, out, code, v, 0, "true", "validated", gated)
}

func TestGitLabCI_NoKeyFailsClosed(t *testing.T) {
	p := newGLProducer(t)
	p.commit("change")
	p.gate(p.cfgDir)
	sha := p.revParse("HEAD")
	out, code, v := glVerify(t, glCheckout(t, p.remote, sha), nil)
	assertGLVerdict(t, out, code, v, 1, "false", "no-key", sha)
}
