package steps

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// CredentialsStep refuses a push whose changed files carry something that looks
// like a live credential.
//
// It exists because of a specific, repeatable trap: a JS step fails for want of
// dependencies, installing them needs auth for a private registry, and the
// command every guide reaches for —
//
//	npm config set //npm.pkg.github.com/:_authToken "$(gh auth token)"
//
// — writes a real token into .npmrc, which most repos TRACK with a
// `${NODE_AUTH_TOKEN}` placeholder in it. The path of least resistance out of a
// red gate therefore ends with a credential staged for commit. A gate should not
// have a happy path that leaks a secret, so warden checks for it itself rather
// than leaving it to an optional recipe.
//
// Scope is deliberately narrow. Only files the change touched are read, and only
// patterns whose shape is unmistakably a credential are matched — a token
// issuer's own prefix, or a PEM private key header. Broad entropy heuristics are
// what make secret scanners hated; a check that cries wolf gets deleted, and
// then it catches nothing. For real coverage (history, entropy, dozens of
// providers) point the `gitleaks` recipe at the repo: `warden recipes gitleaks`.
type CredentialsStep struct{}

// NewCredentialsStep returns the built-in credential check.
func NewCredentialsStep() CredentialsStep { return CredentialsStep{} }

func (CredentialsStep) Name() domain.StepName { return domain.StepCredentials }

// credentialPattern is one recognizable credential shape.
type credentialPattern struct {
	// name is what the developer is told they are about to publish.
	name string
	re   *regexp.Regexp
}

// credentialPatterns are keyed on issuer-assigned prefixes and fixed headers,
// not on entropy or on variable names. Each one is a string that essentially
// cannot occur by accident: if it is in the tree, a credential is in the tree.
var credentialPatterns = []credentialPattern{
	// GitHub's token formats, all prefix-tagged since 2021 precisely so they can
	// be recognized. This is the one the .npmrc trap produces.
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`)},
	{"GitHub fine-grained token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}`)},
	{"AWS access key id", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[0-9A-Za-z-]{10,}`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9]{32,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"Stripe secret key", regexp.MustCompile(`\b[rs]k_live_[0-9A-Za-z]{16,}`)},
	{"npm access token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{"private key", regexp.MustCompile(`-{5}BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-{5}`)},
}

// interpolationRe matches the ways a config file legitimately spells "the
// credential is injected here": shell and CI interpolation. A tracked .npmrc
// carrying `_authToken=${NODE_AUTH_TOKEN}` is the CORRECT state — the very state
// the incident overwrote — so a line that defers to a variable is never a
// finding. Flagging it would train people to ignore this check.
var interpolationRe = regexp.MustCompile(`\$\{[^}]*\}|\$\([^)]*\)|\{\{[^}]*\}\}|<[A-Za-z_][A-Za-z0-9_-]*>`)

// dummyRe matches a credential-shaped string that announces itself as fake:
// AWS's documented AKIA…EXAMPLE key, a row of Xs, "changeme". It is applied
// to the MATCHED TOKEN rather than to the whole line, so a comment that happens
// to contain the word "example" cannot smuggle a real key past the check.
var dummyRe = regexp.MustCompile(`(?i)example|placeholder|redacted|changeme|xxxx|yourtoken|dummy|notreal`)

// maxScanBytes caps how much of a file is read. A credential lives on a line of
// config or source; a file larger than this is a build artifact, a fixture or a
// vendored blob, and reading it wholesale would cost more than it can find.
const maxScanBytes = 1 << 20 // 1 MiB

// maxLineBytes caps a single line, so a minified bundle on one enormous line
// cannot exhaust memory.
const maxLineBytes = 64 << 10

func (s CredentialsStep) Run(_ context.Context, sc application.StepContext) (domain.StepResult, error) {
	var findings []domain.Finding
	for _, rel := range sc.Diff.Paths {
		findings = append(findings, scanFile(sc.WorktreeDir, rel)...)
	}
	if len(findings) > 0 {
		return domain.StepResult{
			Step:     domain.StepCredentials,
			Status:   domain.StepFail,
			Findings: findings,
			Summary:  "credentials: possible secret in a tracked file",
		}, nil
	}
	return domain.StepResult{
		Step:    domain.StepCredentials,
		Status:  domain.StepPass,
		Summary: "credentials: no secrets in the changed files",
	}, nil
}

// scanFile reports every credential-shaped string in one changed file. A file
// that cannot be read — deleted by the change, renamed, unreadable, binary — is
// silently skipped: this step's job is to catch a specific mistake, not to
// second-guess the worktree, and an I/O error must never block a push.
func scanFile(worktreeDir, rel string) []domain.Finding {
	// A path from the diff is repo-relative and must stay inside the worktree;
	// refuse anything that escapes rather than reading an arbitrary file.
	full := filepath.Join(worktreeDir, filepath.Clean(rel))
	if worktreeDir != "" && !strings.HasPrefix(full, filepath.Clean(worktreeDir)+string(os.PathSeparator)) {
		return nil
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() || info.Size() > maxScanBytes {
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var findings []domain.Finding
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if interpolationRe.MatchString(text) {
			continue
		}
		for _, p := range credentialPatterns {
			m := p.re.FindString(text)
			if m == "" || dummyRe.MatchString(m) {
				continue
			}
			findings = append(findings, domain.Finding{
				Severity: domain.SeverityHigh,
				File:     rel,
				Line:     line,
				Message: "looks like a live " + p.name + " (" + redact(m) + "). A tracked file must " +
					"not carry a credential — move it to an environment variable (for npm: export " +
					"NODE_AUTH_TOKEN, never `npm config set …_authToken`, which writes the token " +
					"into .npmrc). If this is a false positive, drop `credentials` from steps in .warden.yaml.",
			})
			// One finding per line is enough to stop the push; listing every
			// pattern that matched the same string would only add noise.
			break
		}
	}
	return findings
}

// redact shows just enough of a match to locate it in the file without
// reprinting the secret into a terminal, a CI log, or warden's own run record.
func redact(s string) string {
	const keep = 7
	if len(s) <= keep {
		return "…"
	}
	return s[:keep] + "…"
}
