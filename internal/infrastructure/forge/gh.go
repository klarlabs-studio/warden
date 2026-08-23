// Package forge adapts a code-hosting provider to the application's Forge port.
// The only implementation is GitHub via the `gh` CLI, so warden inherits gh's
// auth and config rather than embedding tokens or an API client. A repo without
// gh installed simply reports Available() == false and PR creation is skipped.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// GH drives GitHub through the gh CLI, run in the repository directory so gh
// resolves the right remote and auth.
type GH struct {
	dir string
}

// NewGH returns a gh-backed forge rooted at repo dir.
func NewGH(dir string) *GH { return &GH{dir: dir} }

// Available reports whether gh is installed. Auth is checked lazily — an
// unauthenticated gh surfaces its own error on first use, which the runner
// swallows (PR creation is best-effort).
func (g *GH) Available() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func (g *GH) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// runSplit is run with the two streams kept apart. `gh api --include` writes
// the response headers to stdout and its own diagnosis ("gh: Bad credentials
// (HTTP 401)") to stderr; a caller that has to tell "the forge answered 404"
// from "warden could not ask" needs both, and CombinedOutput interleaves them
// unpredictably.
func (g *GH) runSplit(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), strings.TrimSpace(errBuf.String()), err
}

// Reachable reports whether the forge can actually be queried for THIS
// repository: gh installed, authenticated, and holding a credential that can
// read the repo the working directory points at.
//
// Available() is a weaker check on purpose — it answers "is the CLI here",
// which is all a best-effort PR comment needs. Anything that reads the forge
// as EVIDENCE needs this one, because a gh that cannot answer looks exactly
// like a forge with nothing to report: every commit comes back with no pull
// request, and warden would publish that as a finding it never observed.
// It probes with --include, the same way the per-commit lookups do, so a gh
// that cannot produce a readable status line fails HERE — where it stops the
// run with gh's own message — instead of turning every commit in the report
// into an undetermined row.
func (g *GH) Reachable(ctx context.Context) error {
	out, errOut, err := g.runSplit(ctx, "api", "--include", "repos/{owner}/{repo}", "--jq", ".full_name")
	status, body := splitHTTPResponse(out)
	if err != nil || status != 200 {
		// gh's own message names the cause — bad credentials, no GitHub remote,
		// repository not found — far better than anything warden could infer.
		if c := firstLine(errOut); c != "" {
			return errors.New(c)
		}
		if status != 0 {
			return fmt.Errorf("the forge answered HTTP %d for this repository", status)
		}
		if err != nil {
			return err
		}
		return errors.New("gh returned no readable HTTP response")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("gh answered but named no repository")
	}
	return nil
}

// firstLine is the leading non-empty line of s, which is where gh puts the
// human-readable cause.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// EnsurePR returns the open PR for branch, opening one onto base if none
// exists. `gh pr view` succeeds when a PR is already open; otherwise
// `gh pr create --fill` derives title/body from the commits.
func (g *GH) EnsurePR(ctx context.Context, branch, base string) (domain.PRInfo, error) {
	if out, err := g.run(ctx, "pr", "view", branch, "--json", "url,number"); err == nil {
		var pr struct {
			URL    string `json:"url"`
			Number int    `json:"number"`
		}
		if json.Unmarshal([]byte(out), &pr) == nil && pr.URL != "" {
			return domain.PRInfo{URL: pr.URL, Number: pr.Number, Created: false}, nil
		}
	}

	args := []string{"pr", "create", "--head", branch, "--fill"}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		return domain.PRInfo{}, err
	}
	// gh pr create prints the PR URL on its last line.
	url := lastURL(out)
	return domain.PRInfo{URL: url, Created: true}, nil
}

// Comment posts a sticky gate-result comment on branch's PR. It first tries to
// edit the current user's last comment (so repeated pushes update one comment
// instead of stacking); if there is none to edit, it posts a fresh one. Body is
// passed on stdin so multi-line markdown and shell metacharacters are safe.
func (g *GH) Comment(ctx context.Context, branch, body string) error {
	if err := g.runStdin(ctx, body, "pr", "comment", branch, "--edit-last", "--body-file", "-"); err == nil {
		return nil
	}
	return g.runStdin(ctx, body, "pr", "comment", branch, "--body-file", "-")
}

// runStdin runs gh with stdin piped from in, for commands that read a body file
// from "-". Callers only care whether it succeeded, so the combined output is
// consumed to keep it off the terminal rather than returned.
func (g *GH) runStdin(ctx context.Context, in string, args ...string) error {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.dir
	cmd.Stdin = strings.NewReader(in)
	_, err := cmd.CombinedOutput()
	return err
}

// Checks returns the CI status for branch's PR by reading `gh pr checks`'s
// machine-readable JSON. A non-zero exit (failing/pending checks) is expected,
// so the JSON is parsed regardless of exit code.
func (g *GH) Checks(ctx context.Context, branch string) (domain.CIStatus, error) {
	out, _ := g.run(ctx, "pr", "checks", branch, "--json", "state")
	var rows []struct {
		State string `json:"state"`
	}
	if out == "" || json.Unmarshal([]byte(out), &rows) != nil {
		return domain.CIStatus{State: domain.CINone}, nil
	}
	return tally(rows), nil
}

// tally aggregates individual check states into a CIStatus.
func tally(rows []struct {
	State string `json:"state"`
}) domain.CIStatus {
	s := domain.CIStatus{Total: len(rows)}
	for _, r := range rows {
		switch strings.ToUpper(r.State) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			s.Passed++
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED":
			s.Failed++
		default: // PENDING, QUEUED, IN_PROGRESS, EXPECTED, …
			s.Pending++
		}
	}
	switch {
	case s.Total == 0:
		s.State = domain.CINone
	case s.Failed > 0:
		s.State = domain.CIFailing
	case s.Pending > 0:
		s.State = domain.CIPending
	default:
		s.State = domain.CIPassing
	}
	return s
}

// lastURL returns the last whitespace token that looks like a URL, which is how
// gh reports the created PR.
func lastURL(out string) string {
	fields := strings.Fields(out)
	for i := len(fields) - 1; i >= 0; i-- {
		if strings.HasPrefix(fields[i], "http") {
			return fields[i]
		}
	}
	return ""
}
