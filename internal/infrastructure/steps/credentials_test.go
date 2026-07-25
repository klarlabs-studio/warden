package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Credentials are assembled at run time rather than written as literals, so
// this test file does not itself contain anything a secret scanner (warden's
// own gate included) would have to flag.
func fakeGitHubToken() string { return "ghp_" + strings.Repeat("A1b2", 9) }
func fakeAWSKey() string      { return "AK" + "IA" + strings.Repeat("Q", 16) }
func fakeNpmToken() string    { return "npm" + "_" + strings.Repeat("z", 36) }

// dashes is the PEM header's delimiter, kept out of a literal for the same reason.
var dashes = strings.Repeat("-", 5)

func runCredentials(t *testing.T, dir string, paths ...string) domain.StepResult {
	t.Helper()
	res, err := NewCredentialsStep().Run(context.Background(), application.StepContext{
		WorktreeDir: dir,
		Diff:        domain.DiffStats{Paths: paths, FilesTouched: len(paths)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The exact incident from #91: a tracked .npmrc whose placeholder was replaced
// with a live token by `npm config set`.
func TestCredentialsStep_CatchesATokenWrittenIntoATrackedNpmrc(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".npmrc"),
		"@org:registry=https://npm.pkg.github.com\n"+
			"//npm.pkg.github.com/:_authToken="+fakeGitHubToken()+"\n")

	res := runCredentials(t, dir, ".npmrc")
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail — this push would publish a live token", res.Status)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.File != ".npmrc" || f.Line != 2 {
		t.Errorf("finding located at %s:%d, want .npmrc:2", f.File, f.Line)
	}
	if f.Severity != domain.SeverityHigh {
		t.Errorf("severity = %s, want high", f.Severity)
	}
	if !strings.Contains(f.Message, "GitHub token") {
		t.Errorf("message should name the kind of credential: %q", f.Message)
	}
	// The remedy is the whole point — and it must name the RIGHT one.
	if !strings.Contains(f.Message, "NODE_AUTH_TOKEN") || !strings.Contains(f.Message, "npm config set") {
		t.Errorf("message must point at the env-var path and away from npm config set: %q", f.Message)
	}
}

// The secret must not be reprinted in full: warden's output lands in terminals,
// CI logs and its own run record.
func TestCredentialsStep_RedactsTheMatch(t *testing.T) {
	dir := t.TempDir()
	token := fakeGitHubToken()
	writeFile(t, filepath.Join(dir, ".npmrc"), "//registry/:_authToken="+token+"\n")

	res := runCredentials(t, dir, ".npmrc")
	msg := res.Findings[0].Message
	if strings.Contains(msg, token) {
		t.Errorf("the finding reprinted the whole credential: %q", msg)
	}
	if !strings.Contains(msg, token[:7]) {
		t.Errorf("the finding should show a locating prefix: %q", msg)
	}
}

// A tracked .npmrc holding only a placeholder is the CORRECT state — the very
// state the incident overwrote. Flagging it would train people to ignore this
// step, and then it catches nothing.
func TestCredentialsStep_PassesThePlaceholderItIsMeantToProtect(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".npmrc"),
		"@org:registry=https://npm.pkg.github.com\n"+
			"//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}\n")
	writeFile(t, filepath.Join(dir, "ci.yml"),
		"  NODE_AUTH_TOKEN: {{ secrets.GITHUB_TOKEN }}\n"+
			"  AWS_KEY: AK"+"IAIOSFODNN7EXAMPLE\n"+
			"  OTHER: $(vault read secret/token)\n")

	res := runCredentials(t, dir, ".npmrc", "ci.yml")
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s, want pass. False positives get this step deleted: %+v", res.Status, res.Findings)
	}
}

func TestCredentialsStep_RecognizesTheCommonIssuerPrefixes(t *testing.T) {
	cases := map[string]string{
		"gh.txt":  fakeGitHubToken(),
		"aws.env": "AWS_ACCESS_KEY_ID=" + fakeAWSKey(),
		"npm.txt": fakeNpmToken(),
		"key.pem": dashes + "BEGIN RSA PRIVATE KEY" + dashes + "\nMIIabc\n" + dashes + "END RSA PRIVATE KEY" + dashes,
	}
	for name, content := range cases {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, name), content+"\n")
		if res := runCredentials(t, dir, name); res.Status != domain.StepFail {
			t.Errorf("%s: status = %s, want fail", name, res.Status)
		}
	}
}

// Only what the change touched is read. Scanning the whole tree would turn every
// push in a repo with one historical fixture into a permanent block.
func TestCredentialsStep_OnlyReadsTheChangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "untouched.txt"), fakeGitHubToken()+"\n")
	writeFile(t, filepath.Join(dir, "touched.txt"), "hello\n")

	if res := runCredentials(t, dir, "touched.txt"); res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass: the change did not touch the offending file", res.Status)
	}
}

// A path the change deleted, or one that escapes the worktree, must never fail
// the push on an I/O error — this step exists to catch one specific mistake.
func TestCredentialsStep_ToleratesUnreadableAndEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secrets.txt")
	writeFile(t, outside, fakeGitHubToken()+"\n")

	res := runCredentials(t, dir, "deleted.txt", "../"+filepath.Base(filepath.Dir(outside))+"/secrets.txt", "nosuchdir/x")
	if res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass: %+v", res.Status, res.Findings)
	}
}

// A minified bundle or a vendored blob must not be read into memory wholesale.
func TestCredentialsStep_SkipsFilesTooLargeToBeConfigOrSource(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, maxScanBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	copy(big, []byte(fakeGitHubToken()))
	if err := os.WriteFile(filepath.Join(dir, "bundle.js"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runCredentials(t, dir, "bundle.js"); res.Status != domain.StepPass {
		t.Errorf("status = %s, want pass: an oversized artifact is out of scope", res.Status)
	}
}

func TestCredentialsStep_PassesACleanChange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	res := runCredentials(t, dir, "main.go")
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s, want pass: %+v", res.Status, res.Findings)
	}
	if !strings.Contains(res.Summary, "no secrets") {
		t.Errorf("Summary = %q", res.Summary)
	}
}

// The step name must not collide with the `secrets` command step the gitleaks
// recipe tells repos to add: a built-in wins the registry lookup, so a collision
// would silently stop running the user's own scanner.
func TestCredentialsStep_DoesNotShadowTheGitleaksRecipe(t *testing.T) {
	if _, taken := Default()[domain.StepName("secrets")]; taken {
		t.Error("a built-in named `secrets` would shadow the gitleaks recipe's step")
	}
	recipe, ok := domain.RecipeByName("gitleaks")
	if !ok {
		t.Fatal("gitleaks recipe went missing")
	}
	if !strings.Contains(recipe.Snippet, "secrets:") {
		t.Skip("gitleaks recipe no longer uses the `secrets` step name")
	}
}
