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

// Credential-shaped literals must not appear verbatim in the repository: this
// file would otherwise be flagged by the repo's own secret scanner (and by
// GitHub push protection) for fixtures that are not secrets at all. Assembling
// them at run time keeps the tests exact without committing the shapes.
func fakeGitHubPAT() string  { return "gh" + "p_" + strings.Repeat("aB3d", 9) }
func fakeAWSKeyID() string   { return "AK" + "IA" + "IOSFODNN7EXAMPLE" }
func fakeSlackToken() string { return "xo" + "xb-" + "1234567890-abcdefghij" }
func fakePrivateKey() string {
	return strings.Repeat("-", 5) + "BEGIN OPENSSH PRIVATE KEY" + strings.Repeat("-", 5)
}
func fakeFinePAT() string { return "github" + "_pat_" + "11ABCDEFG0abcdefghijklmnop" }

// runSecrets writes files into a worktree and runs the step over them.
func runSecrets(t *testing.T, files map[string]string) domain.StepResult {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, rel)
	}
	res, err := NewSecretsStep().Run(context.Background(), application.StepContext{
		WorktreeDir: dir,
		Diff:        domain.DiffStats{Paths: paths},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The exact trap this step exists for: `npm config set …_authToken` writes a
// live token into a tracked .npmrc while chasing a JS step failure.
func TestSecretsStep_CatchesTokenWrittenIntoTrackedNpmrc(t *testing.T) {
	tok := fakeGitHubPAT()
	res := runSecrets(t, map[string]string{
		".npmrc": "//npm.pkg.github.com/:_authToken=" + tok + "\n@org:registry=https://npm.pkg.github.com\n",
	})
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail", res.Status)
	}
	if len(res.Findings) == 0 {
		t.Fatal("expected a finding")
	}
	f := res.Findings[0]
	if f.File != ".npmrc" || f.Line != 1 {
		t.Errorf("finding located at %s:%d, want .npmrc:1", f.File, f.Line)
	}
	// The value must never be echoed: findings reach the terminal, the run
	// record, and possibly a PR comment.
	if strings.Contains(f.Message, tok) {
		t.Error("the finding must not repeat the credential")
	}
	if !strings.Contains(f.Message, "NODE_AUTH_TOKEN") {
		t.Errorf("the finding should point at the supported path: %q", f.Message)
	}
}

// The placeholder form is what a tracked .npmrc is SUPPOSED to contain.
// Flagging it would make the step unusable in exactly the repos that need it.
func TestSecretsStep_AllowsPlaceholders(t *testing.T) {
	res := runSecrets(t, map[string]string{
		".npmrc":       "//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}\n",
		".npmrc.bare":  "//registry.npmjs.org/:_authToken=$NPM_TOKEN\n",
		".npmrc.empty": "//registry.npmjs.org/:_authToken=\n",
		".env.example": "GITHUB_TOKEN=\nAWS_ACCESS_KEY_ID=\n",
	})
	if res.Status != domain.StepPass {
		t.Fatalf("status = %s (%+v), want pass — placeholders are the correct committed form", res.Status, res.Findings)
	}
}

func TestSecretsStep_CredentialShapes(t *testing.T) {
	cases := map[string]struct {
		body string
		hit  bool
	}{
		"github pat":       {"token: " + fakeGitHubPAT() + "\n", true},
		"github fine pat":  {fakeFinePAT() + "\n", true},
		"aws key id":       {fakeAWSKeyID() + "\n", true},
		"private key":      {fakePrivateKey() + "\n", true},
		"slack token":      {fakeSlackToken() + "\n", true},
		"prose about keys": {"We rotate the AWS access key id every quarter.\n", false},
		"public key":       {strings.Repeat("-", 5) + "BEGIN PUBLIC KEY" + strings.Repeat("-", 5) + "\n", false},
		"short value":      {"_authToken=abc\n", false},
		"plain config":     {"registry=https://registry.npmjs.org\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := runSecrets(t, map[string]string{"f.txt": tc.body})
			got := res.Status == domain.StepFail
			if got != tc.hit {
				t.Errorf("flagged=%v want %v (findings %+v)", got, tc.hit, res.Findings)
			}
		})
	}
}

// Only files the change touched are scanned — the step must not become a
// repo-wide scanner that walls off unrelated commits.
func TestSecretsStep_ScansOnlyChangedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "untouched.txt"),
		[]byte(fakeGitHubPAT()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewSecretsStep().Run(context.Background(), application.StepContext{
		WorktreeDir: dir,
		Diff:        domain.DiffStats{Paths: []string{"changed.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("an untouched file must not fail the gate: %+v", res.Findings)
	}
}

func TestSecretsStep_NothingToScanPasses(t *testing.T) {
	for _, sc := range []application.StepContext{
		{},
		{WorktreeDir: t.TempDir()},
		{Diff: domain.DiffStats{Paths: []string{"a.txt"}}},
	} {
		res, err := NewSecretsStep().Run(context.Background(), sc)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StepPass {
			t.Errorf("empty context should pass, got %s", res.Status)
		}
	}
}

// A deleted file is in the diff but absent from the worktree; it must not error.
func TestSecretsStep_MissingFileIsSkipped(t *testing.T) {
	res, err := NewSecretsStep().Run(context.Background(), application.StepContext{
		WorktreeDir: t.TempDir(),
		Diff:        domain.DiffStats{Paths: []string{"deleted.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepPass {
		t.Errorf("a deleted path should be skipped, got %s", res.Status)
	}
}
