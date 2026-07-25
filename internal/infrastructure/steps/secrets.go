package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// SecretsStep refuses a change in which a TRACKED file carries a live
// credential.
//
// It exists because of a specific, repeatable trap rather than a general wish
// for secret scanning. Getting a JS step to run needs the project's
// dependencies; installing them from a private registry needs auth; and the
// obvious command —
//
//	npm config set //npm.pkg.github.com/:_authToken "$(gh auth token)"
//
// writes a real token into `.npmrc`, which in many repos is a TRACKED file
// holding only a `${NODE_AUTH_TOKEN}` placeholder. So the natural way to make
// warden's own gate pass ends in staging a credential for commit. A gate must
// not have a happy path that leaks a secret.
//
// Scope is deliberately narrow: only files the change touched, only tracked
// ones, and only high-confidence patterns. A false positive here is not a
// nuisance, it is a wall in front of an unrelated commit — the failure mode
// that gets gates bypassed wholesale.
type SecretsStep struct{}

// NewSecretsStep returns the tracked-credential guard.
func NewSecretsStep() SecretsStep { return SecretsStep{} }

func (SecretsStep) Name() domain.StepName { return domain.StepSecrets }

// credentialPattern is one high-confidence shape of a live credential, paired
// with what to tell the developer.
//
// accept, when set, inspects the regex's first capture group and decides
// whether the match is really a secret. Go's RE2 has no lookaround, so
// "an assignment whose value is NOT a ${PLACEHOLDER}" cannot be expressed as a
// pattern; the check lives in code instead of being approximated badly.
type credentialPattern struct {
	name   string
	re     *regexp.Regexp
	hint   string
	accept func(value string) bool
}

// isLiveSecret rejects the forms a tracked config file is SUPPOSED to contain:
// an environment-variable reference, or an empty value.
func isLiveSecret(v string) bool {
	v = strings.Trim(v, `"'`)
	if len(v) < 16 || strings.HasPrefix(v, "$") {
		return false
	}
	// A value that merely mentions a variable (foo${BAR}) is still templated.
	return !strings.Contains(v, "${")
}

// credentialPatterns matches only values that are unambiguously live secrets.
// Placeholders (`${NODE_AUTH_TOKEN}`, `$NPM_TOKEN`, empty values) must NOT
// match: the tracked-`.npmrc`-with-a-placeholder case is exactly the file this
// step is meant to protect, and flagging it as-committed would make the step
// unusable in the repos that need it most.
var credentialPatterns = []credentialPattern{
	{
		name:   "npm registry auth token",
		re:     regexp.MustCompile(`(?i)_auth(?:Token)?\s*=\s*(\S*)`),
		accept: isLiveSecret,
		hint:   "use the NODE_AUTH_TOKEN environment variable instead of `npm config set`, which writes into .npmrc",
	},
	{
		name: "GitHub token",
		re:   regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,}`),
		hint: "revoke it at https://github.com/settings/tokens and pass it via the environment",
	},
	{
		name: "AWS access key id",
		re:   regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		hint: "revoke the key and use a role or an environment variable",
	},
	{
		name: "private key block",
		re:   regexp.MustCompile(`-{5}BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-{5}`),
		hint: "move the key out of the repository and reference it by path",
	},
	{
		name: "Slack token",
		re:   regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
		hint: "revoke it in Slack and pass it via the environment",
	},
}

// maxScanBytes caps how much of a file is examined. A credential lives in the
// first few KB of a config file; reading a multi-megabyte fixture in full would
// cost more than the check is worth.
const maxScanBytes = 512 * 1024

func (s SecretsStep) Run(_ context.Context, sc application.StepContext) (domain.StepResult, error) {
	if sc.WorktreeDir == "" || len(sc.Diff.Paths) == 0 {
		return domain.StepResult{
			Step: domain.StepSecrets, Status: domain.StepPass,
			Summary: "secrets: nothing to scan",
		}, nil
	}

	var findings []domain.Finding
	for _, rel := range sc.Diff.Paths {
		path := filepath.Join(sc.WorktreeDir, filepath.FromSlash(rel))
		// A path outside the worktree is not ours to read; the diff is
		// repo-relative, so this only trips on a malformed entry.
		if !strings.HasPrefix(path, filepath.Clean(sc.WorktreeDir)+string(os.PathSeparator)) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue // deleted in this change, or unreadable — nothing to scan
		}
		if len(data) > maxScanBytes {
			data = data[:maxScanBytes]
		}
		findings = append(findings, scanForCredentials(rel, string(data))...)
	}

	if len(findings) == 0 {
		return domain.StepResult{
			Step: domain.StepSecrets, Status: domain.StepPass,
			Summary: fmt.Sprintf("secrets: %d changed file(s) clean", len(sc.Diff.Paths)),
		}, nil
	}
	return domain.StepResult{
		Step: domain.StepSecrets, Status: domain.StepFail,
		Findings: findings,
		Summary:  fmt.Sprintf("secrets: %d credential(s) in tracked files", len(findings)),
	}, nil
}

// scanForCredentials reports each high-confidence credential in content,
// located by line so the developer can go straight to it.
func scanForCredentials(relPath, content string) []domain.Finding {
	var out []domain.Finding
	for i, line := range strings.Split(content, "\n") {
		for _, p := range credentialPatterns {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if p.accept != nil {
				if len(m) < 2 || !p.accept(m[1]) {
					continue
				}
			}
			out = append(out, domain.Finding{
				Severity: domain.SeverityHigh,
				File:     relPath,
				Line:     i + 1,
				// Never echo the value itself: a finding is printed to the
				// terminal, recorded in the run record, and may be posted to a PR
				// comment — copying the secret into all three would widen the leak
				// this step exists to prevent.
				Message: p.name + " in a tracked file — " + p.hint,
			})
			break // one finding per line is enough to block and to locate it
		}
	}
	return out
}
